package moderation

import (
	"context"
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CaseView читает карточку даже когда предложение скрыто от покупателя.
type CaseView struct {
	Decision *moderation.Decision  `json:"decision"`
	Case     moderation.CaseState  `json:"case"`
	Offer    ReviewCard            `json:"offer"`
	History  []moderation.Decision `json:"history"`
}

// ReviewCard сохраняет форму карточки операторского API.
type ReviewCard = moderation.ReviewCard

// ReviewSeller сохраняет публичные сведения продавца.
type ReviewSeller = moderation.ReviewSeller

// CaseDetails согласует дело, карточку и историю в одной транзакции.
func (s *Service) CaseDetails(ctx context.Context, id ids.UUID) (CaseView, error) {
	var result CaseView
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.ModerationCases().Get(ctx, id)
		if err != nil {
			return err
		}
		result.Case = c.State()
		if result.Case.Card != nil {
			result.Offer = *result.Case.Card
		} else {
			result.Offer, err = reviewCard(ctx, tx, result.Case.TenantID, result.Case.ProductID)
			if err != nil {
				return err
			}
		}

		result.History, err = tx.ModerationDecisions().List(ctx, result.Case.TenantID, result.Case.ProductID)
		if err != nil {
			return err
		}
		for _, d := range result.History {
			if d.CaseID == result.Case.ID {
				result.Decision = &d
				break
			}
		}
		return nil
	})
	return result, err
}

func reviewCard(ctx context.Context, tx ports.Tx, tenant ids.TenantID, product ids.ProductID) (ReviewCard, error) {
	var result ReviewCard
	state, err := tx.Offers().Offer(ctx, tenant, product)
	if err != nil {
		return ReviewCard{}, err
	}
	p := state.Product.State()
	card := p.Card
	offer := catalog.ComposeOffer(state.Product, state.Signals, catalog.Seller{}, state.Verdict)
	result = ReviewCard{
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
	if result.Attributes == nil {
		result.Attributes = map[string]json.RawMessage{}
	}
	if state.Participant != nil {
		seller := state.Participant.State()
		result.Seller = ReviewSeller{Slug: seller.Slug, Name: seller.Name}
	}
	definition, err := tx.Taxonomy().Current(ctx)
	if err != nil {
		return ReviewCard{}, err
	}
	for _, categoryID := range card.Publication.CategoryPath {
		if category, _, ok := definition.Category(categoryID); ok {
			result.CategoryPath = append(result.CategoryPath, category.Name)
		}
	}
	return result, nil
}
