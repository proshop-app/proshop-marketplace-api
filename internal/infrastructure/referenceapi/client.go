// Package referenceapi — потребитель общего реестра MongoDB platform.
// Собственного словаря, поставок, ревизий и записи в PostgreSQL здесь нет.
package referenceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const maxResponseBytes = 64 << 20

var (
	codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	// ErrUnavailable — отказ владельца не превращается в пустой общий словарь.
	ErrUnavailable = errs.New(errs.Unavailable, "reference.unavailable", "общий реестр недоступен")
)

// Config — отдельные права чтения и записи; адрес платформенного сервера.
type Config struct {
	BaseURL    string
	ReadToken  string
	WriteToken string
	Timeout    time.Duration
}

// Client реализует узкие порты реестра, значений схемы и разрешения бренда.
type Client struct {
	baseURL    string
	readToken  string
	writeToken string
	http       *http.Client
}

// New не обращается к сети: запуск marketplace не зависит от bootstrap сервера.
func New(config Config) *Client {
	if config.Timeout <= 0 {
		config.Timeout = 5 * time.Second
	}
	return &Client{
		baseURL:    strings.TrimRight(config.BaseURL, "/"),
		readToken:  config.ReadToken,
		writeToken: config.WriteToken,
		http: &http.Client{
			Timeout: config.Timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Do передаёт JSON и ревизию владельцу. Токены и actor от входящего запроса
// не копируются: их выставляет backend из проверенного контекста.
func (c *Client) Do(ctx context.Context, input ports.ReferenceRequest) (ports.ReferenceResponse, error) {
	if !codePattern.MatchString(input.Dictionary) || strings.Contains(input.Resource, "..") {
		return ports.ReferenceResponse{}, ErrUnavailable
	}
	token := c.readToken
	if input.Write {
		token = c.writeToken
	}
	if c.baseURL == "" || token == "" {
		return ports.ReferenceResponse{}, ErrUnavailable
	}
	path := c.baseURL + "/api/v1/internal/reference/" + url.PathEscape(input.Dictionary) + "/" + input.Resource
	if input.Query != "" {
		path += "?" + input.Query
	}
	request, err := http.NewRequestWithContext(ctx, input.Method, path, bytes.NewReader(input.Body))
	if err != nil {
		return ports.ReferenceResponse{}, ErrUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	if input.IfMatch != "" {
		request.Header.Set("If-Match", input.IfMatch)
	}
	if input.Write && input.Actor != "" {
		request.Header.Set("X-Reference-Actor", input.Actor)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return ports.ReferenceResponse{}, ErrUnavailable.Wrap(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes || !json.Valid(body) || response.StatusCode >= 500 ||
		response.StatusCode < 200 || response.StatusCode >= 300 && response.StatusCode < 400 {
		return ports.ReferenceResponse{}, ErrUnavailable
	}
	if response.StatusCode >= 400 {
		body = compatibleError(body)
	}
	return ports.ReferenceResponse{
		Status: response.StatusCode,
		Body:   body,
		ETag:   response.Header.Get("ETag"),
	}, nil
}

func compatibleError(body []byte) []byte {
	var flat struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &flat) != nil || len(flat.Error) > 0 {
		return body
	}
	if flat.Code == "reference.key_taken" {
		flat.Code = "REFERENCE_KEY_TAKEN"
	}
	if flat.Code == "reference.entry_changed" {
		flat.Code = "REFERENCE_ENTRY_CHANGED"
	}
	result, _ := json.Marshal(map[string]any{"error": map[string]string{"code": flat.Code, "message": flat.Message}})
	return result
}
