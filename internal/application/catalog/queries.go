package catalog

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
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

// OfferCard — предложение с подписями тегов.
type OfferCard struct {
	Offer ports.OfferDocument
	Tags  []catalog.TagLabel
}

// OfferList — страница выдачи с подписями тегов.
type OfferList struct {
	Total int
	Items []OfferCard
}

// Queries — чтение выдачи и карточек.
type Queries struct {
	uow   ports.UnitOfWork
	index ports.OfferIndex
}

// NewQueries — запросы каталога.
func NewQueries(uow ports.UnitOfWork, index ports.OfferIndex) *Queries {
	return &Queries{uow: uow, index: index}
}

// Search — страница выдачи с подписями тегов. Пустые лимит и порядок берутся по умолчанию.
func (q *Queries) Search(ctx context.Context, query ports.OfferQuery) (OfferList, error) {
	normalized, err := normalize(query)
	if err != nil {
		return OfferList{}, err
	}

	page, err := q.index.Search(ctx, normalized)
	if err != nil {
		return OfferList{}, err
	}

	tagsByTenant := map[ids.TenantID][]string{}
	for _, doc := range page.Items {
		tenant := ids.TenantID(doc.TenantID)
		tagsByTenant[tenant] = append(tagsByTenant[tenant], doc.Tags...)
	}

	labels, err := q.labels(ctx, tagsByTenant)
	if err != nil {
		return OfferList{}, err
	}

	list := OfferList{Total: page.Total, Items: make([]OfferCard, 0, len(page.Items))}
	for _, doc := range page.Items {
		list.Items = append(list.Items, OfferCard{Offer: doc, Tags: catalog.LabelTags(doc.Tags, labels[ids.TenantID(doc.TenantID)])})
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

	labels, err := q.labels(ctx, map[ids.TenantID][]string{offerID.Tenant: doc.Tags})
	if err != nil {
		return OfferCard{}, err
	}

	return OfferCard{Offer: doc, Tags: catalog.LabelTags(doc.Tags, labels[offerID.Tenant])}, nil
}

// labels — подписи тегов из справочников магазинов одной транзакцией чтения.
func (q *Queries) labels(ctx context.Context, tags map[ids.TenantID][]string) (map[ids.TenantID]map[string]string, error) {
	labels := make(map[ids.TenantID]map[string]string, len(tags))

	wanted := make(map[ids.TenantID][]string, len(tags))
	for tenant, list := range tags {
		if len(list) > 0 {
			wanted[tenant] = list
		}
	}
	if len(wanted) == 0 {
		return labels, nil
	}

	err := q.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for tenant, list := range wanted {
			tenantLabels, err := tx.Tags().Labels(ctx, tenant, list)
			if err != nil {
				return err
			}

			labels[tenant] = tenantLabels
		}

		return nil
	})

	return labels, err
}

func normalize(query ports.OfferQuery) (ports.OfferQuery, error) {
	query.Text = strings.TrimSpace(query.Text)

	if utf8.RuneCountInString(query.Text) > MaxQueryLength {
		return query, ErrInvalidQuery.WithMessage("строка поиска длиннее 200 символов")
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
