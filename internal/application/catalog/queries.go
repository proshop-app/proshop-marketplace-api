package catalog

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// DefaultLimit — размер страницы выдачи по умолчанию.
	DefaultLimit = 20
	// MaxLimit — наибольший размер страницы.
	MaxLimit = 100
	// MaxWindow — глубже этой позиции выдача не листается.
	MaxWindow = 2400
	// MaxQueryLength — длина строки поиска в символах.
	MaxQueryLength = 200
	// MaxQueryWords — число слов в строке поиска.
	MaxQueryWords = 32
	// MaxTenants — предел магазинов в фильтре каталога.
	MaxTenants = 100
)

// ErrInvalidQuery — параметры выдачи некорректны.
var ErrInvalidQuery = errs.New(errs.Invalid, "catalog.invalid_query", "некорректные параметры выдачи")

// OfferCard — проверенное предложение каталога.
type OfferCard struct {
	AttributeDetails map[string]AttributeDisplay
	SellersCount     *int
	Offer            ports.OfferDocument
}

// OfferList — страница выдачи и фасеты.
type OfferList struct {
	FacetCategoryID    string
	AvailableFacetKeys []string
	Correction         *ports.OfferCorrection
	Receipt            string
	Facets             []ports.Facet
	Total              int
	TotalRelation      string
	Limit              int
	Limited            bool
	Items              []OfferCard
}

// Queries — чтение выдачи и карточек.
type Queries struct {
	display             atomic.Pointer[taxonomyDisplay]
	displayMu           sync.Mutex
	clock               ports.Clock
	recommendationLoads map[string]*recommendationLoad
	recommendationMu    sync.Mutex
	recommendationCache map[string]recommendationCacheEntry
	platform            ports.PlatformAPI
	interests           ports.BuyerInterests
	recommendations     ports.RecommendationOffers
	uow                 ports.UnitOfWork
	index               ports.OfferIndex
}

// PublicationView объединяет структурные и модерационные причины скрытия.
type PublicationView struct {
	taxonomy.Publication
	Moderation ModerationView `json:"moderation"`
}

// ModerationView показывает отсутствие оценки без выдуманного вердикта.
type ModerationView struct {
	Status  moderation.Status   `json:"status"`
	Reasons []moderation.Reason `json:"reasons"`
}

// NewQueries — запросы каталога.
func NewQueries(
	uow ports.UnitOfWork,
	index ports.OfferIndex,
	platform ports.PlatformAPI,
	interests ports.BuyerInterests,
	recommendations ports.RecommendationOffers,
	clock ports.Clock,
) *Queries {
	return &Queries{
		clock:           clock,
		uow:             uow,
		index:           index,
		platform:        platform,
		interests:       interests,
		recommendations: recommendations,
	}
}

// Search — страница выдачи каталога. Пустые лимит и порядок берутся по умолчанию.
func (q *Queries) Search(ctx context.Context, query ports.OfferQuery) (OfferList, error) {
	normalized, presentation, err := q.prepareQuery(ctx, query)
	if err != nil {
		return OfferList{}, err
	}
	page, err := q.index.Search(ctx, normalized)
	if err != nil {
		return OfferList{}, err
	}
	var facetCategoryID string
	if normalized.FacetTaxonomyVersion > 0 {
		if panel, ok := presentation.taxonomy.panels[page.FacetCategoryID]; ok && page.FacetCategoryID != "" {
			facetCategoryID = page.FacetCategoryID
			presentation = presentation.taxonomy.resultFacetDisplay(normalized, panel)
		}
	}
	list := OfferList{
		FacetCategoryID: facetCategoryID,
		Correction:      page.Correction,
		Facets:          displayFacets(page.Facets, normalized, presentation),
		Total:           page.Total,
		TotalRelation:   page.TotalRelation,
		Limit:           normalized.Limit,
		Limited:         page.Limited,
		Items:           make([]OfferCard, 0, len(page.Items)),
	}
	if len(normalized.FacetKeys) == 0 {
		for _, filter := range presentation.order {
			key := string(wireBuiltin(filter.Code))
			if key == "" {
				key = "attr." + filter.Code
			}
			if normalized.FacetTaxonomyVersion == 0 || page.AvailableFacetKeys == nil ||
				slices.Contains(page.AvailableFacetKeys, key) {
				list.AvailableFacetKeys = append(list.AvailableFacetKeys, key)
			}
		}
	}
	if normalized.Observe && len(page.Items) != 0 {
		list.Receipt = page.Receipt
	}
	for _, doc := range page.Items {
		list.Items = append(
			list.Items,
			OfferCard{
				Offer: doc,
			},
		)
	}

	return list, nil
}

// Offer — карточка предложения; скрытое предложение не отдаётся.
func (q *Queries) Offer(ctx context.Context, offerID ids.OfferID) (OfferCard, error) {
	doc, found, err := q.index.Get(ctx, offerID.String())
	if err != nil {
		return OfferCard{}, err
	}
	if !found || !doc.Visible {
		return OfferCard{}, catalog.ErrOfferNotFound
	}

	return OfferCard{
		Offer:            doc,
		AttributeDetails: q.display.Load().attributes(doc),
	}, nil
}

func normalize(query ports.OfferQuery) (ports.OfferQuery, error) {
	if err := validateFacetSelection(query.Facets); err != nil {
		return query, err
	}
	if err := validateListingFacetKeys(query.FacetKeys); err != nil {
		return query, err
	}
	text, err := NormalizeQueryText(query.Text)
	if err != nil {
		return query, err
	}
	query.Text = text
	query.TrackQuery = query.TrackQuery && query.Text != "" && query.Offset == 0 && len(query.FacetKeys) == 0
	query.Observe = query.Observe && query.Text != "" && len(query.FacetKeys) == 0
	if !query.Observe {
		query.BuyerKey = ""
	}

	if len(query.TenantIDs) > MaxTenants {
		return query, ErrInvalidQuery.WithMessage("слишком много магазинов")
	}
	tenants := make([]string, 0, len(query.TenantIDs))
	seen := make(map[string]bool, len(query.TenantIDs))
	for _, tenant := range query.TenantIDs {
		if _, err := ids.ParseTenantID(tenant); err != nil {
			return query, ErrInvalidQuery.WithMessage("некорректный магазин")
		}
		if !seen[tenant] {
			tenants = append(tenants, tenant)
			seen[tenant] = true
		}
	}
	query.TenantIDs = tenants
	if query.GroupID != "" && (len(query.TenantIDs) != 1 || len(query.GroupID) > 128) {
		return query, ErrInvalidQuery.WithMessage("группа требует ровно один магазин и корректный идентификатор")
	}
	if query.Limit == 0 {
		query.Limit = DefaultLimit
	}
	if query.Limit < 0 || query.Limit > MaxLimit {
		return query, ErrInvalidQuery.WithMessage("размер страницы — от 1 до 100")
	}
	if query.Offset < 0 || query.Offset >= MaxWindow {
		return query, ErrInvalidQuery.WithMessage("доступны первые 2400 позиций выдачи, уточните фильтры")
	}
	query.Limit = min(query.Limit, MaxWindow-query.Offset)
	if (query.PriceMin != nil && *query.PriceMin < 0) || (query.PriceMax != nil && *query.PriceMax < 0) {
		return query, ErrInvalidQuery.WithMessage("цена не может быть отрицательной")
	}
	if query.PriceMin != nil && query.PriceMax != nil && *query.PriceMin > *query.PriceMax {
		return query, ErrInvalidQuery.WithMessage("нижняя граница цены больше верхней")
	}

	switch query.Sort {
	case "":
		query.Sort = ports.SortRelevance
	case ports.SortRelevance, ports.SortPriceAsc, ports.SortPriceDesc, ports.SortNewest, ports.SortRating:
	default:
		return query, ErrInvalidQuery.WithMessage("неизвестный порядок выдачи")
	}

	return query, nil
}

// Publication — диагностика из проекции, включая скрытые предложения; транспорт закрыт токеном оператора.
func (q *Queries) Publication(ctx context.Context, id ids.OfferID) (PublicationView, error) {
	var publication PublicationView
	err := q.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		product, err := tx.Products().Get(ctx, id.Tenant, id.Product)
		if err != nil {
			return err
		}
		publication.Publication = product.State().Card.Publication
		publication.Moderation.Reasons = []moderation.Reason{moderation.NotEvaluated()}
		verdict, err := tx.ModerationVerdicts().Get(ctx, id.Tenant, id.Product)
		if err != nil && !errors.Is(err, moderation.ErrNotFound) {
			return err
		}
		if verdict != nil {
			publication.Moderation.Status = verdict.State().Status
			publication.Moderation.Reasons = verdict.State().Reasons
		}
		return nil
	})
	return publication, err
}

// PrepareQuery сохраняет одинаковые фильтры для органики и рекламы.
func (q *Queries) PrepareQuery(ctx context.Context, query ports.OfferQuery) (ports.OfferQuery, error) {
	normalized, _, err := q.prepareQuery(ctx, query)
	return normalized, err
}

func (q *Queries) prepareQuery(ctx context.Context, query ports.OfferQuery) (ports.OfferQuery, facetDisplay, error) {
	normalized, err := normalize(query)
	if err != nil {
		return ports.OfferQuery{}, facetDisplay{}, err
	}

	var presentation facetDisplay
	if err := q.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		d, err := tx.Taxonomy().Current(ctx)
		if err != nil {
			return err
		}
		view := q.displayFor(d)
		panel, found := view.panels[normalized.CategoryID]
		if !found {
			return ErrInvalidQuery.WithMessage("неизвестная категория")
		}
		presentation, err = view.prepareFacetPanel(&normalized, panel)
		if err != nil {
			return err
		}
		return nil
	}); err != nil {
		return ports.OfferQuery{}, facetDisplay{}, err
	}
	return normalized, presentation, nil
}
