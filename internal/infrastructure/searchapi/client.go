// Package searchapi — адаптер общего поискового движка uno.
package searchapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	contractVersion = 5
	apiPath         = "/api/v1/internal/marketplace"
	batchLimit      = 200
	requestLimit    = 4 << 20
	responseLimit   = 16 << 20
)

var (
	// ErrUnavailable — поисковый сервис не подтвердил операцию.
	ErrUnavailable = errs.New(errs.Unavailable, "search.unavailable", "поиск площадки недоступен")
	// ErrInvalid — запрос нельзя передать в поиск.
	ErrInvalid = errs.New(errs.Invalid, "search.invalid_request", "некорректный запрос поиска")
)

// Config — адрес и отдельный служебный токен поискового API.
type Config struct {
	URL     string
	Token   string
	Timeout time.Duration
}

// Index реализует порт индекса предложений через proshop-search.
type Index struct {
	base   string
	token  string
	client *http.Client
}

// New создаёт клиент с таймаутом и запретом переноса токена через redirect.
func New(cfg Config) (*Index, error) {
	parsed, err := url.Parse(cfg.URL)

	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || len(cfg.Token) < 32 || cfg.Timeout <= 0 {
		return nil, fmt.Errorf("некорректные настройки поискового сервиса")
	}

	return &Index{
		base:  strings.TrimRight(cfg.URL, "/") + apiPath,
		token: cfg.Token,
		client: &http.Client{
			Timeout: cfg.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Ping проверяет доступность управления каталогом.
func (x *Index) Ping(ctx context.Context) error {
	ready, err := x.Exists(ctx)

	if err != nil {
		return err
	}

	if !ready {
		return ErrUnavailable.WithMessage("рабочее поколение поиска не готово")
	}

	return nil
}

// Exists сообщает, есть ли рабочее поколение.
func (x *Index) Exists(ctx context.Context) (bool, error) {
	var result struct {
		Ready           bool `json:"ready"`
		ContractVersion int  `json:"contractVersion"`
	}
	_, err := x.request(ctx, http.MethodGet, "/status", nil, &result)

	if err != nil {
		return false, err
	}

	if result.ContractVersion != contractVersion {
		return false, ErrUnavailable.WithMessage("поиск не поддерживает текущий контракт каталога")
	}

	return result.Ready, nil
}

// NewGeneration создаёт или возобновляет строящееся поколение.
func (x *Index) NewGeneration(ctx context.Context) (string, error) {
	var result struct {
		Generation string `json:"generation"`
	}
	_, err := x.request(ctx, http.MethodPost, "/generations", nil, &result)

	if err != nil {
		return "", err
	}

	if !validGeneration(result.Generation) {
		return "", ErrUnavailable.WithMessage("поиск вернул некорректное поколение")
	}

	return result.Generation, nil
}

// Activate переключает выдачу после завершения наполнения.
func (x *Index) Activate(ctx context.Context, generation string) error {
	if !validGeneration(generation) {
		return ErrInvalid
	}
	_, err := x.request(ctx, http.MethodPost, "/generations/"+generation+"/activate", nil, nil)

	return err
}

// Put доставляет события во все текущие цели записи.
func (x *Index) Put(ctx context.Context, docs []ports.OfferDocument) error {
	return x.put(ctx, "/offers", docs)
}

// PutGeneration наполняет выбранную текущую сборку.
func (x *Index) PutGeneration(ctx context.Context, generation string, docs []ports.OfferDocument) error {
	if !validGeneration(generation) {
		return ErrInvalid
	}

	return x.put(ctx, "/generations/"+generation+"/offers", docs)
}

func (x *Index) put(ctx context.Context, path string, docs []ports.OfferDocument) error {
	for offset := 0; offset < len(docs); {
		batch, err := nextOfferBatch(docs[offset:])

		if err != nil {
			return err
		}

		if _, err := x.request(ctx, http.MethodPost, path, batch, nil); err != nil {
			return err
		}
		offset += len(batch.Offers)
	}

	return nil
}

// Get возвращает только публичное предложение с ожидаемой идентичностью.
func (x *Index) Get(ctx context.Context, id string) (ports.OfferDocument, bool, error) {
	identity, err := ids.ParseOfferID(id)

	if err != nil {
		return ports.OfferDocument{}, false, ErrInvalid.Wrap(err)
	}
	var result offerDocument
	status, err := x.request(
		ctx,
		http.MethodPost,
		"/offer",
		offerIdentity{
			TenantID:  string(identity.Tenant),
			ProductID: string(identity.Product),
		},
		&result,
	)

	if status == http.StatusNotFound {
		return ports.OfferDocument{}, false, nil
	}

	if err != nil {
		return ports.OfferDocument{}, false, err
	}

	if result.TenantID != string(identity.Tenant) || result.ProductID != string(identity.Product) {
		return ports.OfferDocument{}, false, ErrUnavailable.WithMessage("поиск вернул другое предложение")
	}

	doc, err := fromDocument(result)

	return doc, err == nil, err
}

// Search передаёт текст, каноническую категорию и фильтры общему движку.
func (x *Index) Search(ctx context.Context, query ports.OfferQuery) (ports.OfferPage, error) {
	var result offerPage
	_, err := x.request(
		ctx,
		http.MethodPost,
		"/search",
		offerQuery{
			TrackQuery:    query.TrackQuery,
			PriceContexts: wirePriceContexts(query.PriceContexts),
			Attributes:    query.Attributes,
			FacetFields:   wireFacetFields(query),
			Facets:        wireFacetSelection(query.Facets),
			InStockSet:    query.InStockSet,
			Text:          query.Text,
			TenantIDs:     query.TenantIDs,
			GroupID:       query.GroupID,
			CategoryID:    query.CategoryID,
			PriceMin:      query.PriceMin,
			PriceMax:      query.PriceMax,
			InStock:       query.InStock,
			Sort:          string(query.Sort),
			Offset:        query.Offset,
			Limit:         query.Limit,
		},
		&result,
	)

	if err != nil {
		return ports.OfferPage{}, err
	}
	correction, err := fromCorrection(result.Correction)
	if err != nil {
		return ports.OfferPage{}, err
	}
	page := ports.OfferPage{
		Correction: correction,
		Facets:     result.Facets,
		Total:      result.Total,
		Items:      make([]ports.OfferDocument, 0, len(result.Items)),
	}

	for _, doc := range result.Items {
		converted, err := fromDocument(doc)

		if err != nil {
			return ports.OfferPage{}, err
		}
		page.Items = append(page.Items, converted)
	}

	return page, nil
}

// NeedsRebuild читает решение о росте из общего поискового сервиса.
func (x *Index) NeedsRebuild(ctx context.Context) (bool, error) {
	var result struct {
		RebuildRequired bool `json:"rebuildRequired"`
	}
	_, err := x.request(ctx, http.MethodGet, "/maintenance", nil, &result)

	return result.RebuildRequired, err
}
