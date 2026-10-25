// Package identityhub — backend code/PKCE обмен и проверка ES256 JWT одного realm.
package identityhub

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const (
	maxResponse   = 1 << 20
	keyCacheTTL   = 5 * time.Minute
	keyRetryDelay = 5 * time.Second
)

// ErrUnavailable не содержит upstream body/URL/credential.
var ErrUnavailable = errs.New(errs.Unavailable, "identity.unavailable", "выдача токенов сейчас недоступна")

// Config задаётся доверенным окружением; http.Client обязан проверять TLS.
type Config struct {
	Realm    string
	URL      string
	ClientID string
	Secret   string
}

// Client никогда не следует URL из токена или пользовательского запроса.
type Client struct {
	config    Config
	issuer    string
	http      *http.Client
	now       func() time.Time
	mu        sync.Mutex
	keys      map[string]publicKey
	keysUntil time.Time
	retryAt   time.Time
	unknownAt time.Time
}

type codeResponse struct {
	Code     string `json:"code"`
	Issuer   string `json:"issuer"`
	ClientID string `json:"client_id"`
	Redirect string `json:"redirect_uri"`
}

type tokenResponse struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	ID      string `json:"id_token"`
	Type    string `json:"token_type"`
}

// New использует фиксированные OIDC пути identity-hub, без discovery с чужими URL.
func New(config Config, transport *http.Client) *Client {
	client := *transport
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &Client{
		config: config,
		issuer: strings.TrimRight(config.URL, "/") + "/realms/" + config.Realm,
		http:   &client,
		now:    time.Now,
		keys:   make(map[string]publicKey),
	}
}

// Open обменивает локальное подтверждение на code с S256 и проверяет ID token.
func (c *Client) Open(ctx context.Context, id string) (ports.HubTokens, error) {
	verifier, err := secret()
	if err != nil {
		return ports.HubTokens{}, err
	}

	nonce, err := secret()
	if err != nil {
		return ports.HubTokens{}, err
	}

	state, err := secret()
	if err != nil {
		return ports.HubTokens{}, err
	}

	body, err := json.Marshal(map[string]string{
		"client_id":      c.config.ClientID,
		"subject":        id,
		"code_challenge": digest(verifier),
		"nonce":          nonce,
		"state":          state,
	})
	if err != nil {
		return ports.HubTokens{}, ErrUnavailable
	}

	endpoint := strings.TrimRight(c.config.URL, "/") + "/backend/realms/" + c.config.Realm + "/authorize"
	var code codeResponse
	if err := c.request(ctx, endpoint, body, "application/json", c.config.Secret, &code); err != nil {
		return ports.HubTokens{}, err
	}

	redirect := "https://backend.identity.invalid/" + c.config.ClientID
	if code.Code == "" || code.Issuer != c.issuer || code.ClientID != c.config.ClientID || code.Redirect != redirect {
		return ports.HubTokens{}, buyer.ErrSessionNotFound
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {c.config.ClientID},
		"redirect_uri":  {redirect},
		"code":          {code.Code},
		"code_verifier": {verifier},
	}
	response, err := c.exchange(ctx, form)
	if err != nil {
		return ports.HubTokens{}, err
	}

	claims, err := c.Claims(ctx, response.Access, false)
	if err != nil {
		return ports.HubTokens{}, err
	}

	identity, err := c.verify(ctx, response.ID, false, false)
	if err != nil {
		return ports.HubTokens{}, err
	}
	if identity.Nonce != nonce || identity.Subject != claims.Subject ||
		identity.AccessHash != accessHash(response.Access) {
		return ports.HubTokens{}, buyer.ErrSessionNotFound
	}

	return ports.HubTokens{
		Access:  response.Access,
		Refresh: response.Refresh,
		Claims:  claims,
	}, nil
}

// Refresh не повторяет запрос при ошибке/таймауте: результат ротации неизвестен.
func (c *Client) Refresh(ctx context.Context, token, subject string) (ports.HubTokens, error) {
	response, err := c.exchange(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {c.config.ClientID},
		"refresh_token": {token},
	})
	if err != nil {
		return ports.HubTokens{}, err
	}

	claims, err := c.Claims(ctx, response.Access, false)
	if err != nil {
		return ports.HubTokens{}, err
	}
	if claims.Subject != subject {
		return ports.HubTokens{}, buyer.ErrSessionNotFound
	}

	return ports.HubTokens{
		Access:  response.Access,
		Refresh: response.Refresh,
		Claims:  claims,
	}, nil
}

// Revoke — RFC7009, только фиксированный endpoint своего issuer.
func (c *Client) Revoke(ctx context.Context, token string) error {
	form := url.Values{
		"token":           {token},
		"token_type_hint": {"refresh_token"},
		"client_id":       {c.config.ClientID},
	}

	return c.request(ctx, c.issuer+"/revoke", []byte(form.Encode()), "application/x-www-form-urlencoded", "", nil)
}

func (c *Client) exchange(ctx context.Context, form url.Values) (tokenResponse, error) {
	var response tokenResponse
	err := c.request(
		ctx,
		c.issuer+"/oauth/token",
		[]byte(form.Encode()),
		"application/x-www-form-urlencoded",
		"",
		&response,
	)
	if err != nil {
		return tokenResponse{}, err
	}

	if response.Access == "" || response.Refresh == "" || !strings.EqualFold(response.Type, "Bearer") {
		return tokenResponse{}, buyer.ErrSessionNotFound
	}

	return response, nil
}

func (c *Client) request(
	ctx context.Context,
	endpoint string,
	body []byte,
	contentType, credential string,
	out any,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ErrUnavailable
	}

	req.Header.Set("Content-Type", contentType)
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}

	response, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnauthorized ||
		response.StatusCode == http.StatusForbidden {
		return buyer.ErrSessionNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrUnavailable
	}
	if out == nil {
		return nil
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil || len(raw) > maxResponse || json.Unmarshal(raw, out) != nil {
		return ErrUnavailable
	}

	return nil
}

func secret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", ErrUnavailable
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func digest(value string) string {
	hash := sha256.Sum256([]byte(value))

	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func accessHash(value string) string {
	hash := sha256.Sum256([]byte(value))

	return base64.RawURLEncoding.EncodeToString(hash[:16])
}
