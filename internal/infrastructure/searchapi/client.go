// Package searchapi — адаптер общего поискового движка uno.
package searchapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/httpclient"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	contractVersion         = 12
	generationSchemaVersion = 11
	apiPath                 = "/api/v1/internal/marketplace"
	batchLimit              = 200
	requestLimit            = 4 << 20
	responseLimit           = 16 << 20
	minTokenLength          = 32

	// ServiceTimeout — потолок служебного вызова: пачки outbox, поколения и пересборка,
	// проверки и справочные чтения. Пачка до 4 МиБ ждёт refresh поколения в поиске.
	ServiceTimeout = 35 * time.Second
	// buyerTimeout — срок выдачи, карточки и группы для покупателя. Движок сам режет
	// запрос в индекс на 1,5 с (searchTimeout в proshop-search); сверху — сеть и разбор
	// ответа. Зависший поиск не держит HTTP-запросы покупателей до их общего срока.
	buyerTimeout = 3 * time.Second
	// searchConns — соединения реплики к поиску: восемь групп outbox, четыре полосы
	// покупок и выдача покупателям. Больше — реплика перегружена: вызов ждёт соединение
	// в пределах своего срока, а не открывает сокеты без предела.
	searchConns = 64
)

var (
	// ErrUnavailable — поисковый сервис не подтвердил операцию.
	ErrUnavailable = errs.New(errs.Unavailable, "search.unavailable", "поиск площадки недоступен")
	// ErrInvalid — запрос нельзя передать в поиск.
	ErrInvalid = errs.New(errs.Invalid, "search.invalid_request", "некорректный запрос поиска")
)

// Config — адрес и отдельный служебный токен поискового API.
type Config struct {
	URL   string
	Token string
}

// Index реализует порт индекса предложений через proshop-search.
type Index struct {
	base            string
	token           string
	client          *http.Client
	referenceBearer string
}

// New создаёт клиент со своим транспортом и запретом переноса токена через redirect.
func New(cfg Config) (*Index, error) {
	parsed, err := url.Parse(cfg.URL)

	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || len(cfg.Token) < minTokenLength {
		return nil, fmt.Errorf("некорректные настройки поискового сервиса")
	}

	return &Index{
		base:   strings.TrimRight(cfg.URL, "/") + apiPath,
		token:  cfg.Token,
		client: httpclient.Internal(httpclient.Config{Conns: searchConns, Timeout: ServiceTimeout}),
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
		Ready                   bool `json:"ready"`
		ContractVersion         int  `json:"contractVersion"`
		GenerationSchemaVersion int  `json:"generationSchemaVersion"`
	}
	_, err := x.request(ctx, http.MethodGet, "/status", nil, &result)

	if err != nil {
		return false, err
	}

	if result.ContractVersion != contractVersion || result.GenerationSchemaVersion != generationSchemaVersion {
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

// Get возвращает только публичное предложение с ожидаемой идентичностью.
func (x *Index) Get(ctx context.Context, id string) (ports.OfferDocument, bool, error) {
	identity, err := ids.ParseOfferID(id)

	if err != nil {
		return ports.OfferDocument{}, false, ErrInvalid.Wrap(err)
	}
	var result offerDocument
	status, err := x.read(
		ctx,
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
	payload := offerQuery{
		Exact:                query.Exact,
		TrackQuery:           query.TrackQuery,
		Observe:              query.Observe,
		BuyerKey:             query.BuyerKey,
		Attributes:           query.Attributes,
		FacetFields:          wireFacetFields(query),
		FacetTaxonomyVersion: query.FacetTaxonomyVersion,
		PanelAttributes:      query.FacetTaxonomyVersion > 0 && query.WantsFacet(ports.FacetAttributes),
		Facets:               wireFacetSelection(query.Facets),
		FacetKeys:            query.FacetKeys,
		PriceHistogram:       query.PriceHistogram,
		InStockSet:           query.InStockSet,
		Text:                 query.Text,
		TenantIDs:            query.TenantIDs,
		GroupID:              query.GroupID,
		CategoryID:           query.CategoryID,
		PriceMin:             query.PriceMin,
		PriceMax:             query.PriceMax,
		InStock:              query.InStock,
		Sort:                 string(query.Sort),
		Offset:               query.Offset,
		Limit:                query.Limit,
	}
	var result offerPage
	status, err := x.read(ctx, "/search", payload, &result)
	if status == http.StatusBadRequest && payload.Observe {
		slog.WarnContext(ctx, "поиск отклонил наблюдение: повтор запроса без observe")
		payload.Observe = false
		payload.BuyerKey = ""
		status, err = x.read(ctx, "/search", payload, &result)
	}

	if status == http.StatusBadRequest {
		return ports.OfferPage{}, ErrInvalid
	}
	if err != nil {
		return ports.OfferPage{}, err
	}
	correction, err := fromCorrection(result.Correction)
	if err != nil {
		return ports.OfferPage{}, err
	}
	page := ports.OfferPage{
		Correction:         correction,
		FacetCategoryID:    result.FacetCategoryID,
		AvailableFacetKeys: result.AvailableFacetKeys,
		Receipt:            result.Receipt,
		Facets:             result.Facets,
		TotalRelation:      result.TotalRelation,
		Limited:            result.Limited,
		Total:              result.Total,
		Items:              make([]ports.OfferDocument, 0, len(result.Items)),
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
