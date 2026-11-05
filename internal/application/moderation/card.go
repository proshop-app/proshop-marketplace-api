package moderation

import (
	"context"
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CaseView читает карточку даже когда предложение скрыто от покупателя.
type CaseView struct {
	Case    moderation.CaseState  `json:"case"`
	Offer   ReviewCard            `json:"offer"`
	History []moderation.Decision `json:"history"`
}

// ReviewCard использует опубликованные атрибуты и действующую цену проекции.
type ReviewCard struct {
	Name         string                     `json:"name"`
	Description  string                     `json:"description"`
	Image        string                     `json:"image"`
	Price        int64                      `json:"price"`
	BasePrice    int64                      `json:"basePrice"`
	CategoryID   string                     `json:"categoryId"`
	CategoryPath []string                   `json:"categoryPath"`
	Attributes   map[string]json.RawMessage `json:"attributes"`
	Seller       ReviewSeller               `json:"seller"`
	Publication  taxonomy.Publication       `json:"publication"`
}

// ReviewSeller ограничивает сведения о продавце публичными полями.
type ReviewSeller struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// CaseDetails согласует дело, карточку и историю в одной транзакции.
func (s *Service) CaseDetails(ctx context.Context, id ids.UUID) (CaseView, error) {
	var result CaseView
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.ModerationCases().Get(ctx, id)
		if err != nil {
			return err
		}
		result.Case = c.State()
		state, err := tx.Offers().Offer(ctx, result.Case.TenantID, result.Case.ProductID)
		if err != nil {
			return err
		}
		p := state.Product.State()
		card := p.Card
		offer := catalog.ComposeOffer(state.Product, state.Signals, catalog.Seller{}, state.Verdict)
		result.Offer = ReviewCard{
			Name:         card.Name,
			Description:  card.Description,
			Image:        card.Image,
			Price:        offer.Price.Kopecks(),
			BasePrice:    offer.BasePrice.Kopecks(),
			CategoryID:   card.Publication.CategoryID,
			CategoryPath: []string{},
			Attributes:   card.Publication.Attributes,
			Publication:  card.Publication,
		}
		if result.Offer.Attributes == nil {
			result.Offer.Attributes = map[string]json.RawMessage{}
		}
		if state.Participant != nil {
			seller := state.Participant.State()
			result.Offer.Seller = ReviewSeller{Slug: seller.Slug, Name: seller.Name}
		}
		definition, err := tx.Taxonomy().Current(ctx)
		if err != nil {
			return err
		}
		for _, categoryID := range card.Publication.CategoryPath {
			if category, _, ok := definition.Category(categoryID); ok {
				result.Offer.CategoryPath = append(result.Offer.CategoryPath, category.Name)
			}
		}
		result.History, err = tx.ModerationDecisions().List(ctx, result.Case.TenantID, result.Case.ProductID)
		return err
	})
	return result, err
}
