// Package synonymmodel вызывает необязательный HTTP-провайдер номинаций.
package synonymmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/synonyms"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// RequestTimeout — срок ответа внешней модели на пачку понятий: генерация идёт десятки
// секунд, а фоновая сверка синонимов не держит покупателей и может подождать.
const RequestTimeout = 35 * time.Second

const (
	requestLimit  = 16 << 10
	responseLimit = 32 << 10
	prompt        = "Найди точные альтернативные названия перечисленных видов товаров. " +
		"Не предлагай род, подвид, аксессуар, совместное упоминание, бренд или опечатку. " +
		"Используй каноническое имя вида в to. Верни до 20 направлений или пустой массив."
)

var errProvider = errs.New(errs.Unavailable, "search_synonyms.model_unavailable", "модель синонимов недоступна")

// Client передаёт только имена и определения из опубликованной таксономии.
type Client struct {
	url   string
	token string
	model string
	http  *http.Client
}

type request struct {
	Model         string                      `json:"model"`
	PromptVersion string                      `json:"promptVersion"`
	Prompt        string                      `json:"prompt"`
	Concepts      []ports.SynonymModelConcept `json:"concepts"`
}

type response struct {
	Proposals []ports.SynonymModelProposal `json:"proposals"`
}

// New требует HTTPS и не переносит учётные данные провайдера через перенаправление.
func New(endpoint, token, model string, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Fragment != "" || parsed.RawQuery != "" ||
		strings.TrimSpace(model) == "" || len(model) > 128 || timeout <= 0 {
		return nil, fmt.Errorf("некорректные настройки провайдера поисковых синонимов")
	}
	return &Client{
		url:   endpoint,
		token: token,
		model: model,
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Identity отделяет ключи идемпотентности разных моделей и провайдеров.
func (c *Client) Identity() string { return c.url + "\x00" + c.model }

// Propose запрашивает ограниченный ответ для проверенного набора понятий.
func (c *Client) Propose(
	ctx context.Context,
	id string,
	concepts []ports.SynonymModelConcept,
) ([]ports.SynonymModelProposal, error) {
	body, err := json.Marshal(request{
		Model:         c.model,
		PromptVersion: synonyms.ModelPromptVersion,
		Prompt:        prompt,
		Concepts:      concepts,
	})
	if err != nil {
		return nil, err
	}
	if len(body) > requestLimit {
		return nil, errProvider
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, errProvider
	}
	req.Header.Set("Content-Type", "application/json")
	// Повтор провайдера должен занять отдельное место в суточной квоте.
	req.GetBody = nil
	req.Header.Set("Idempotency-Key", id)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	reply, err := c.http.Do(req)
	if err != nil {
		return nil, errProvider
	}
	defer func() { _ = reply.Body.Close() }()
	return readReply(reply)
}

func readReply(reply *http.Response) ([]ports.SynonymModelProposal, error) {
	if reply.StatusCode != http.StatusOK {
		return nil, errProvider
	}
	raw, err := io.ReadAll(io.LimitReader(reply.Body, responseLimit+1))
	if err != nil || len(raw) > responseLimit {
		return nil, errProvider
	}
	var result response
	if err := json.Unmarshal(raw, &result); err != nil || result.Proposals == nil {
		return nil, errProvider
	}
	return result.Proposals, nil
}
