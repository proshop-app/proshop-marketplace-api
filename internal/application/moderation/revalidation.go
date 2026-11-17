package moderation

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type revalidationInputs struct {
	members map[ids.TenantID]*participant.Participant
	data    map[ids.OfferID]ports.RevalidationModeration
	rules   moderation.Rules
}

// PlanRevalidation читает страницу пачкой и передаёт значения тому же решению, что приём события.
func (s *Service) PlanRevalidation(
	ctx context.Context,
	tx ports.Tx,
	products []*catalog.Product,
	definition taxonomy.Definition,
	checker taxonomy.Checker,
	now time.Time,
) ([]ports.RevalidationChange, error) {
	page, err := s.readRevalidationInputs(ctx, tx, products)
	if err != nil {
		return nil, err
	}
	result := make([]ports.RevalidationChange, 0, len(products))
	for _, product := range products {
		state := product.State()
		data := page.data[ids.OfferID{Tenant: state.TenantID, Product: state.ProductID}]
		change := ports.RevalidationChange{Product: product, Failure: data.Failure}
		if change.Failure == "" {
			input := evaluationInput{
				Product:     product,
				Participant: page.members[state.TenantID],
				Definition:  definition,
				Policy:      checker.ModerationPolicy(state.Card.Publication.CategoryID),
				Rules:       page.rules,
				Verdict:     data.Verdict,
				Case:        data.Case,
				Signals:     data.Signals,
				Now:         now,
			}
			decision := s.completeEvaluation(input, decideVerdict(input))
			change.Verdict, change.Case = decision.Verdict, decision.Case
			change.Observations = decision.Observations
		}
		result = append(result, change)
	}
	return result, nil
}

func (s *Service) readRevalidationInputs(
	ctx context.Context,
	tx ports.Tx,
	products []*catalog.Product,
) (revalidationInputs, error) {
	page := revalidationInputs{}
	tenants, keys := revalidationKeys(products)
	var err error
	if len(tenants) > 0 {
		page.members, err = tx.Participants().Batch(ctx, tenants)
		if err != nil {
			return page, err
		}
	}
	if len(page.members) > 0 {
		page.rules, err = tx.ModerationRules().Current(ctx)
		if err != nil {
			return page, err
		}
		page.rules.RequireImage = s.config.RequireImage
	}
	page.data, err = tx.Revalidation().Moderation(ctx, keys)
	return page, err
}

func revalidationKeys(products []*catalog.Product) ([]ids.TenantID, []ids.OfferID) {
	tenants, keys := []ids.TenantID{}, []ids.OfferID{}
	seen := map[ids.TenantID]bool{}
	for _, product := range products {
		state := product.State()
		keys = append(keys, ids.OfferID{Tenant: state.TenantID, Product: state.ProductID})
		if state.Card.Publication.Published && !seen[state.TenantID] {
			tenants = append(tenants, state.TenantID)
			seen[state.TenantID] = true
		}
	}
	return tenants, keys
}
