// Package opensearch — индекс предложений маркетплейса. Рабочий индекс —
// алиас над поколением; перестроение заводит новое поколение и переключает
// алиас атомарно.
package opensearch

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	opensearchgo "github.com/opensearch-project/opensearch-go/v4"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// ErrUnavailable — кластер недоступен или ответил ошибкой.
var ErrUnavailable = errs.New(errs.Unavailable, "index.unavailable", "индекс предложений недоступен")

// Config — подключение и параметры поколений.
type Config struct {
	URLs               []string
	Username           string
	Password           string
	InsecureSkipVerify bool
	Alias              string
	Shards             int
	Replicas           int
}

// Index — индекс предложений в OpenSearch.
type Index struct {
	client   *opensearchgo.Client
	alias    string
	shards   int
	replicas int
	now      func() time.Time
}

// New — клиент индекса.
func New(cfg Config) (*Index, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // явно включается окружением для локального кластера с самоподписанным сертификатом
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

	return &Index{client: client, alias: cfg.Alias, shards: cfg.Shards, replicas: cfg.Replicas, now: time.Now}, nil
}

// Ping — кластер отвечает.
func (x *Index) Ping(ctx context.Context) error {
	_, err := x.do(ctx, http.MethodGet, "/_cluster/health", nil, nil)

	return err
}

// Exists — алиас рабочего индекса заведён.
func (x *Index) Exists(ctx context.Context) (bool, error) {
	status, err := x.do(ctx, http.MethodHead, "/_alias/"+url.PathEscape(x.alias), nil, nil, http.StatusNotFound)
	if err != nil {
		return false, err
	}

	return status == http.StatusOK, nil
}

// NewGeneration заводит пустое поколение со схемой.
func (x *Index) NewGeneration(ctx context.Context) (string, error) {
	name := fmt.Sprintf("%s-%s", x.alias, x.now().UTC().Format("20060102t150405.000000000"))
	name = strings.ReplaceAll(name, ".", "")

	if _, err := x.do(ctx, http.MethodPut, "/"+url.PathEscape(name), indexBody(x.shards, x.replicas), nil); err != nil {
		return "", err
	}

	return name, nil
}

// Activate переключает алиас на поколение одним действием и удаляет прежние
// поколения, в том числе брошенные прерванным перестроением.
func (x *Index) Activate(ctx context.Context, generation string) error {
	aliased, err := x.aliased(ctx)
	if err != nil {
		return err
	}

	all, err := x.generations(ctx)
	if err != nil {
		return err
	}

	actions := []any{map[string]any{"add": map[string]any{"index": generation, "alias": x.alias, "is_write_index": true}}}
	for _, name := range aliased {
		if name != generation {
			actions = append(actions, map[string]any{"remove": map[string]any{"index": name, "alias": x.alias}})
		}
	}

	if _, err := x.do(ctx, http.MethodPost, "/_aliases", map[string]any{"actions": actions}, nil); err != nil {
		return err
	}

	for _, name := range all {
		if name == generation {
			continue
		}
		if _, err := x.do(ctx, http.MethodDelete, "/"+url.PathEscape(name), nil, nil, http.StatusNotFound); err != nil {
			return err
		}
	}

	return nil
}

// aliased — поколения под алиасом.
func (x *Index) aliased(ctx context.Context) ([]string, error) {
	var aliases map[string]json.RawMessage

	status, err := x.do(ctx, http.MethodGet, "/_alias/"+url.PathEscape(x.alias), nil, &aliases, http.StatusNotFound)
	if err != nil || status == http.StatusNotFound {
		return nil, err
	}

	names := make([]string, 0, len(aliases))
	for name := range aliases {
		names = append(names, name)
	}

	return names, nil
}

// generations — все поколения индекса по имени, с алиасом и без.
func (x *Index) generations(ctx context.Context) ([]string, error) {
	var rows []struct {
		Index string `json:"index"`
	}

	if _, err := x.do(ctx, http.MethodGet, "/_cat/indices/"+url.PathEscape(x.alias+"-*")+"?format=json&h=index", nil, &rows); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Index)
	}

	return names, nil
}

// Put пишет документы в рабочий индекс.
func (x *Index) Put(ctx context.Context, docs []ports.OfferDocument) error {
	return x.PutGeneration(ctx, x.alias, docs)
}

// PutGeneration пишет документы с внешней версией: документ с версией ниже
// записанной кластер отклоняет, и это не ошибка — в индексе уже свежее.
func (x *Index) PutGeneration(ctx context.Context, target string, docs []ports.OfferDocument) error {
	if len(docs) == 0 {
		return nil
	}

	var body bytes.Buffer
	encoder := json.NewEncoder(&body)

	for _, doc := range docs {
		meta := map[string]any{"index": map[string]any{
			"_index": target, "_id": doc.ID, "version": doc.Version, "version_type": "external_gte",
		}}
		if err := encoder.Encode(meta); err != nil {
			return fmt.Errorf("пачка индекса: %w", err)
		}
		if err := encoder.Encode(toDocument(doc)); err != nil {
			return fmt.Errorf("пачка индекса: %w", err)
		}
	}

	var response struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			ID     string `json:"_id"`
			Status int    `json:"status"`
			Error  *struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"error"`
		} `json:"items"`
	}

	if _, err := x.raw(ctx, http.MethodPost, "/_bulk", &body, "application/x-ndjson", &response); err != nil {
		return err
	}
	if !response.Errors {
		return nil
	}

	for _, item := range response.Items {
		for _, result := range item {
			if result.Error == nil || result.Status == http.StatusConflict {
				continue
			}

			return ErrUnavailable.WithMessage(fmt.Sprintf("документ %s не записан: %s: %s", result.ID, result.Error.Type, result.Error.Reason))
		}
	}

	return nil
}

// Get — документ рабочего индекса.
func (x *Index) Get(ctx context.Context, id string) (ports.OfferDocument, bool, error) {
	var response struct {
		Found   bool     `json:"found"`
		Version int64    `json:"_version"`
		Source  document `json:"_source"`
	}

	status, err := x.do(ctx, http.MethodGet, "/"+url.PathEscape(x.alias)+"/_doc/"+url.PathEscape(id), nil, &response, http.StatusNotFound)
	if err != nil {
		return ports.OfferDocument{}, false, err
	}
	if status == http.StatusNotFound || !response.Found {
		return ports.OfferDocument{}, false, nil
	}

	return fromDocument(response.Source, response.Version), true, nil
}

// Search — выдача.
func (x *Index) Search(ctx context.Context, query ports.OfferQuery) (ports.OfferPage, error) {
	var response struct {
		Hits struct {
			Total struct {
				Value int `json:"value"`
			} `json:"total"`
			Hits []struct {
				Version int64    `json:"_version"`
				Source  document `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}

	if _, err := x.do(ctx, http.MethodPost, "/"+url.PathEscape(x.alias)+"/_search", searchBody(query), &response); err != nil {
		return ports.OfferPage{}, err
	}

	page := ports.OfferPage{Total: response.Hits.Total.Value, Items: make([]ports.OfferDocument, 0, len(response.Hits.Hits))}
	for _, hit := range response.Hits.Hits {
		page.Items = append(page.Items, fromDocument(hit.Source, hit.Version))
	}

	return page, nil
}

// Refresh делает записанное видимым поиску сразу; нужен проверкам, рабочий путь на него не опирается.
func (x *Index) Refresh(ctx context.Context) error {
	_, err := x.do(ctx, http.MethodPost, "/"+url.PathEscape(x.alias)+"/_refresh", nil, nil)

	return err
}

func (x *Index) do(ctx context.Context, method, path string, payload any, out any, accepted ...int) (int, error) {
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

func (x *Index) raw(ctx context.Context, method, path string, body io.Reader, contentType string, out any, accepted ...int) (int, error) {
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

		return response.StatusCode, ErrUnavailable.WithMessage(fmt.Sprintf("%s %s: %d %s", method, path, response.StatusCode, strings.TrimSpace(string(detail))))
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
