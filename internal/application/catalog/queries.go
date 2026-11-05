package catalog

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

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
	MaxWindow = 10000
	// MaxQueryLength — длина строки поиска в символах.
	MaxQueryLength = 200
)

// ErrInvalidQuery — параметры выдачи некорректны.
var ErrInvalidQuery = errs.New(errs.Invalid, "catalog.invalid_query", "некорректные параметры выдачи")

// OfferCard — проверенное предложение каталога.
type OfferCard struct {
	Variants *VariantBlock
	Offer    ports.OfferDocument
}

// OfferList — страница выдачи и фасеты.
type OfferList struct {
	Facets []ports.Facet
	Total  int
	Items  []OfferCard
}

// Queries — чтение выдачи и карточек.
type Queries struct {
	uow     ports.UnitOfWork
	index   ports.OfferIndex
	pricing ports.CatalogPricing
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
func NewQueries(uow ports.UnitOfWork, index ports.OfferIndex, pricing ports.CatalogPricing) *Queries {
	return &Queries{uow: uow, index: index, pricing: pricing}
}

// Search — страница выдачи каталога. Пустые лимит и порядок берутся по умолчанию.
func (q *Queries) Search(ctx context.Context, query ports.OfferQuery) (OfferList, error) {
	normalized, err := normalize(query)
	if err != nil {
		return OfferList{}, err
	}

	if err := q.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		d, err := tx.Taxonomy().Current(ctx)
		if err != nil {
			return err
		}
		if query.CategoryID != "" {
			if _, _, ok := d.Category(query.CategoryID); !ok {
				return ErrInvalidQuery.WithMessage("неизвестная категория")
			}
		}
		fields := d.Fields()
		if query.CategoryID != "" {
			fields = categoryFields(d, query.CategoryID)
		}
		if err := validateAttributeFilters(normalized.Attributes, fields); err != nil {
			return err
		}
		for code, filter := range normalized.Attributes {
			filter.Type = fields[code].Type
			normalized.Attributes[code] = filter
		}
		for _, a := range fields {
			if a.Facet {
				normalized.FacetFields = append(normalized.FacetFields, a)
			}
		}
		sort.Slice(normalized.FacetFields, func(i, j int) bool {
			return normalized.FacetFields[i].Code < normalized.FacetFields[j].Code
		})
		return nil
	}); err != nil {
		return OfferList{}, err
	}
	normalized.PriceContexts, err = q.priceContexts(ctx, normalized.BuyerID, normalized.TenantID)
	if err != nil {
		return OfferList{}, err
	}
	page, err := q.index.Search(ctx, normalized)
	if err != nil {
		return OfferList{}, err
	}
	list := OfferList{
		Facets: visibleFacets(page.Facets, page.Total),
		Total:  page.Total,
		Items:  make([]OfferCard, 0, len(page.Items)),
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
func (q *Queries) Offer(ctx context.Context, offerID ids.OfferID, buyerID ids.UUID) (OfferCard, error) {
	doc, found, err := q.index.Get(ctx, offerID.String())
	if err != nil {
		return OfferCard{}, err
	}
	if !found || !doc.Visible {
		return OfferCard{}, catalog.ErrOfferNotFound
	}

	contexts, err := q.priceContexts(ctx, buyerID, string(offerID.Tenant))
	if err != nil {
		return OfferCard{}, err
	}
	key := catalog.DefaultPriceProfile
	if contexts != nil {
		key = contexts[offerID.Tenant]
	}
	profile, found := catalog.SelectPrice(doc.PriceProfiles, key)
	if !found {
		return OfferCard{}, catalog.ErrOfferNotFound
	}
	doc.Price = profile.EffectivePrice.Kopecks()
	doc.BasePrice = profile.Price.Kopecks()

	variants, err := q.offerVariants(ctx, doc, key)
	if err != nil {
		return OfferCard{}, err
	}
	return OfferCard{
		Offer:    doc,
		Variants: variants,
	}, nil
}

func normalize(query ports.OfferQuery) (ports.OfferQuery, error) {
	query.Text = strings.TrimSpace(query.Text)

	if utf8.RuneCountInString(query.Text) > MaxQueryLength {
		return query, ErrInvalidQuery.WithMessage("строка поиска длиннее 200 символов")
	}
	if query.GroupID != "" && (query.TenantID == "" || len(query.GroupID) > 128) {
		return query, ErrInvalidQuery.WithMessage("группа требует магазин и корректный идентификатор")
	}
	if query.TenantID != "" {
		if _, err := ids.ParseTenantID(query.TenantID); err != nil {
			return query, ErrInvalidQuery.WithMessage("некорректный магазин")
		}
	}
	if query.Limit == 0 {
		query.Limit = DefaultLimit
	}
	if query.Limit < 0 || query.Limit > MaxLimit {
		return query, ErrInvalidQuery.WithMessage("размер страницы — от 1 до 100")
	}
	if query.Offset < 0 || query.Offset+query.Limit > MaxWindow {
		return query, ErrInvalidQuery.WithMessage("выдача листается не глубже 10000 предложений")
	}
	if (query.PriceMin != nil && *query.PriceMin < 0) || (query.PriceMax != nil && *query.PriceMax < 0) {
		return query, ErrInvalidQuery.WithMessage("цена не может быть отрицательной")
	}
	if query.PriceMin != nil && query.PriceMax != nil && *query.PriceMin > *query.PriceMax {
		return query, ErrInvalidQuery.WithMessage("нижняя граница цены больше верхней")
	}

	switch query.Sort {
	case "":
		query.Sort = ports.SortRelevance
	case ports.SortRelevance, ports.SortPriceAsc, ports.SortPriceDesc, ports.SortNewest:
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

// categoryFields объединяет собранные схемы потомков для фасетов раздела.
func categoryFields(d taxonomy.Definition, id string) map[string]taxonomy.Attribute {
	fields := map[string]taxonomy.Attribute{}
	var collect func(string)
	collect = func(id string) {
		category, _, found := d.Category(id)
		if !found {
			return
		}
		for _, attribute := range category.Attributes {
			fields[attribute.Code] = attribute.Attribute
		}
		for _, child := range category.Children {
			collect(child.ID)
		}
	}
	collect(id)
	return fields
}
