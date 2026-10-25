package identityhub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
)

type publicKey = *ecdsa.PublicKey

type jwtHeader struct {
	Algorithm string   `json:"alg"`
	KeyID     string   `json:"kid"`
	Critical  []string `json:"crit"`
}

type jwtClaims struct {
	Issuer     string          `json:"iss"`
	Subject    string          `json:"sub"`
	Audience   json.RawMessage `json:"aud"`
	ClientID   string          `json:"client_id"`
	Realm      string          `json:"realm"`
	Tenant     string          `json:"tid"`
	JTI        string          `json:"jti"`
	Expires    int64           `json:"exp"`
	Issued     int64           `json:"iat"`
	NotBefore  int64           `json:"nbf"`
	Nonce      string          `json:"nonce"`
	AccessHash string          `json:"at_hash"`
}

type jwksDocument struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Type      string `json:"kty"`
	Curve     string `json:"crv"`
	KeyID     string `json:"kid"`
	Algorithm string `json:"alg"`
	Use       string `json:"use"`
	X         string `json:"x"`
	Y         string `json:"y"`
}

// Claims проверяет подпись и все границы до различения истёкшего access.
func (c *Client) Claims(ctx context.Context, token string, allowExpired bool) (ports.HubClaims, error) {
	claims, err := c.verify(ctx, token, allowExpired, true)
	if err != nil {
		return ports.HubClaims{}, err
	}

	return ports.HubClaims{
		Subject:   claims.Subject,
		ExpiresAt: time.Unix(claims.Expires, 0),
		IssuedAt:  time.Unix(claims.Issued, 0),
	}, nil
}

func (c *Client) verify(ctx context.Context, token string, allowExpired, accessOnly bool) (jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 16384 {
		return jwtClaims{}, buyer.ErrSessionNotFound
	}

	var header jwtHeader
	if !decode(parts[0], &header) || header.Algorithm != "ES256" || header.KeyID == "" || len(header.Critical) > 0 {
		return jwtClaims{}, buyer.ErrSessionNotFound
	}

	// Сначала разбираем формат, чтобы мусор не инициировал JWKS-запрос.
	var claims jwtClaims
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if !decode(parts[1], &claims) || err != nil || len(signature) != 64 {
		return jwtClaims{}, buyer.ErrSessionNotFound
	}

	key, err := c.key(ctx, header.KeyID)
	if err != nil {
		return jwtClaims{}, err
	}

	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	first := new(big.Int).SetBytes(signature[:32])
	second := new(big.Int).SetBytes(signature[32:])
	if !ecdsa.Verify(key, hash[:], first, second) {
		return jwtClaims{}, buyer.ErrSessionNotFound
	}

	now := c.now().Unix()
	if claims.Issuer != c.issuer || claims.Subject == "" || claims.ClientID != c.config.ClientID ||
		claims.Realm != c.config.Realm || claims.Tenant != "" || !hasAudience(claims.Audience, c.config.ClientID) ||
		claims.Issued <= 0 || claims.Issued > now || claims.NotBefore > now || claims.Expires <= claims.Issued ||
		(accessOnly && claims.JTI == "") {
		return jwtClaims{}, buyer.ErrSessionNotFound
	}

	if claims.Expires <= now && !allowExpired {
		return jwtClaims{}, buyer.ErrAccessExpired
	}

	return claims, nil
}

func (c *Client) key(ctx context.Context, kid string) (publicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	known := c.keys[kid]
	expired := !now.Before(c.keysUntil)
	unknownDue := known == nil && !now.Before(c.unknownAt)
	if (expired || unknownDue) && !now.Before(c.retryAt) {
		c.unknownAt = now.Add(keyRetryDelay)
		if err := c.loadKeys(ctx); err != nil {
			c.retryAt = now.Add(keyRetryDelay)
			if known == nil {
				return nil, err
			}
		} else {
			c.keysUntil = now.Add(keyCacheTTL)
			c.retryAt = time.Time{}
			known = c.keys[kid]
		}
	}

	if known == nil {
		if now.Before(c.retryAt) {
			return nil, ErrUnavailable
		}

		return nil, buyer.ErrSessionNotFound
	}

	return known, nil
}

func (c *Client) loadKeys(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.issuer+"/keys", nil)
	if err != nil {
		return ErrUnavailable
	}

	response, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return ErrUnavailable
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	var document jwksDocument
	if err != nil || len(raw) > maxResponse || json.Unmarshal(raw, &document) != nil {
		return ErrUnavailable
	}

	keys := make(map[string]publicKey)
	for _, candidate := range document.Keys {
		if candidate.Type != "EC" || candidate.Curve != "P-256" || candidate.KeyID == "" ||
			(candidate.Algorithm != "" && candidate.Algorithm != "ES256") ||
			(candidate.Use != "" && candidate.Use != "sig") {
			continue
		}

		x, xerr := base64.RawURLEncoding.DecodeString(candidate.X)
		y, yerr := base64.RawURLEncoding.DecodeString(candidate.Y)
		if xerr != nil || yerr != nil || len(x) != 32 || len(y) != 32 {
			return ErrUnavailable
		}

		pointX, pointY := new(big.Int).SetBytes(x), new(big.Int).SetBytes(y)
		if !elliptic.P256().IsOnCurve(pointX, pointY) || keys[candidate.KeyID] != nil {
			return ErrUnavailable
		}

		keys[candidate.KeyID] = &ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     pointX,
			Y:     pointY,
		}
	}

	if len(keys) == 0 {
		return ErrUnavailable
	}

	c.keys = keys

	return nil
}

func decode(value string, target any) bool {
	raw, err := base64.RawURLEncoding.DecodeString(value)

	return err == nil && json.Unmarshal(raw, target) == nil
}

func hasAudience(raw json.RawMessage, expected string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == expected
	}

	var list []string
	if json.Unmarshal(raw, &list) != nil || len(list) != 1 {
		return false
	}

	return list[0] == expected
}
