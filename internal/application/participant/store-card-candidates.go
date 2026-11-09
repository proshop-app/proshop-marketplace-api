package participant

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// CardCandidatesPageSize ограничивает одну страницу выбора опубликованных товаров.
	CardCandidatesPageSize = 20
	// CardCandidatesQueryMax ограничивает длину строки поиска.
	CardCandidatesQueryMax = 200
)

// ErrInvalidCardCandidates не допускает неоднозначные фильтры и неограниченную выдачу.
var ErrInvalidCardCandidates = errs.New(
	errs.Invalid,
	"participant.invalid_card_candidates",
	"некорректные параметры выбора товаров карточки",
)

// CardCandidatesQuery не читает чужой каталог: магазин задаёт доверенный маршрут.
type CardCandidatesQuery struct {
	Query string
	IDs   []string
	Page  int
	Limit int
}

// CardCandidates — опубликованные и доступные предложения без денежных полей.
type CardCandidates struct {
	Items []ports.StoreCardProduct `json:"items"`
	Total int                      `json:"total"`
	Page  int                      `json:"page"`
	Limit int                      `json:"limit"`
}

func normalizeCardCandidates(query CardCandidatesQuery) (CardCandidatesQuery, error) {
	if query.Page == 0 {
		query.Page = 1
	}
	if query.Limit == 0 {
		query.Limit = CardCandidatesPageSize
	}
	query.Query = strings.TrimSpace(query.Query)
	if query.Page < 1 || query.Limit < 1 || query.Limit > CardCandidatesPageSize ||
		utf8.RuneCountInString(query.Query) > CardCandidatesQueryMax ||
		(query.Query != "" && len(query.IDs) != 0) || len(query.IDs) > storeProductsSize {
		return query, ErrInvalidCardCandidates
	}
	seen := map[string]bool{}
	for _, id := range query.IDs {
		if _, err := ids.ParseProductID(id); err != nil || seen[id] {
			return query, ErrInvalidCardCandidates
		}
		seen[id] = true
	}
	return query, nil
}

// CardCandidates использует те же текущие ограничения видимости, что пересчёт карточки.
func (s *Service) CardCandidates(
	ctx context.Context,
	tenant ids.TenantID,
	query CardCandidatesQuery,
) (CardCandidates, error) {
	query, err := normalizeCardCandidates(query)
	if err != nil {
		return CardCandidates{}, err
	}
	result := CardCandidates{
		Items: []ports.StoreCardProduct{},
		Page:  query.Page,
		Limit: query.Limit,
	}
	err = s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, tenant)
		if err != nil {
			return err
		}
		visible, _, _, err := storeOffers(ctx, tx, p, nil)
		if err != nil {
			return err
		}
		sortStoreOffers(visible)
		products := cardCandidatesProducts(visible, query)
		result.Total = len(products)
		// Сначала проверяем номер страницы: произведение page*limit не переполняет int.
		if len(products) == 0 || query.Page > (len(products)-1)/query.Limit+1 {
			return nil
		}
		start := (query.Page - 1) * query.Limit
		end := min(start+query.Limit, len(products))
		result.Items = products[start:end]
		return nil
	})
	return result, err
}

func cardCandidatesProducts(items []storeOffer, query CardCandidatesQuery) []ports.StoreCardProduct {
	products := []ports.StoreCardProduct{}
	if len(query.IDs) != 0 {
		for _, id := range query.IDs {
			for _, item := range items {
				if string(item.product.ProductID) == id {
					products = append(products, storeProduct(item))
					break
				}
			}
		}
		return products
	}
	text := strings.ToLower(query.Query)
	for _, item := range items {
		name := strings.ToLower(item.product.Card.Name)
		id := strings.ToLower(string(item.product.ProductID))
		if text == "" || strings.Contains(name, text) || strings.Contains(id, text) {
			products = append(products, storeProduct(item))
		}
	}
	return products
}
