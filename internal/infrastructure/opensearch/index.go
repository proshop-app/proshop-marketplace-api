// Package opensearch — географический индекс пунктов выдачи маркетплейса.
package opensearch

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	opensearchgo "github.com/opensearch-project/opensearch-go/v4"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// ErrUnavailable — кластер недоступен или ответил ошибкой.
var ErrUnavailable = errs.New(errs.Unavailable, "index.unavailable", "индекс пунктов выдачи недоступен")

// Config — подключение и параметры поколений.
type Config struct {
	URLs               []string
	Username           string
	Password           string
	InsecureSkipVerify bool
	Shards             int
	Replicas           int
}

// Client — подключение к геоиндексу пунктов выдачи.
type Client struct {
	client   *opensearchgo.Client
	shards   int
	replicas int
}

// New — клиент индекса.
func New(cfg Config) (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.InsecureSkipVerify {
		// явно включается окружением для локального кластера с самоподписанным сертификатом
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // проверку выключает только настройка окружения
		}
	}

	client, err := opensearchgo.NewClient(opensearchgo.Config{
		Addresses: cfg.URLs,
		Username:  cfg.Username,
		Password:  cfg.Password,
		Transport: transport,
	})
	if err != nil {
		return nil, fmt.Errorf("клиент индекса: %w", err)
	}

	return &Client{
		client:   client,
		shards:   cfg.Shards,
		replicas: cfg.Replicas,
	}, nil
}

// Ping — кластер отвечает.
func (x *Client) Ping(ctx context.Context) error {
	_, err := x.do(ctx, http.MethodGet, "/_cluster/health", nil, nil)

	return err
}

func (x *Client) do(ctx context.Context, method, path string, payload any, out any, accepted ...int) (int, error) {
	var body io.Reader

	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, fmt.Errorf("запрос индекса: %w", err)
		}

		body = bytes.NewReader(encoded)
	}

	return x.raw(ctx, method, path, body, "application/json", out, accepted...)
}

func (x *Client) raw(
	ctx context.Context,
	method, path string,
	body io.Reader,
	contentType string,
	out any,
	accepted ...int,
) (int, error) {
	request, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return 0, fmt.Errorf("запрос индекса: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", contentType)
	}

	response, err := x.client.Perform(request)
	if err != nil {
		return 0, ErrUnavailable.Wrap(err)
	}
	defer func() { _ = response.Body.Close() }()

	for _, status := range accepted {
		if response.StatusCode == status {
			_, _ = io.Copy(io.Discard, response.Body)

			return status, nil
		}
	}

	if response.StatusCode >= http.StatusMultipleChoices {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 2048))

		message := fmt.Sprintf("%s %s: %d %s", method, path, response.StatusCode, strings.TrimSpace(string(detail)))

		return response.StatusCode, ErrUnavailable.WithMessage(message)
	}

	if out == nil || method == http.MethodHead {
		_, _ = io.Copy(io.Discard, response.Body)

		return response.StatusCode, nil
	}

	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return response.StatusCode, fmt.Errorf("ответ индекса %s %s: %w", method, path, err)
	}

	return response.StatusCode, nil
}
