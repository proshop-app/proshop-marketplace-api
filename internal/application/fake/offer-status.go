package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (o offers) StatusBatch(ctx context.Context, keys []ids.OfferID) ([]ports.OfferStatus, error) {
	states, err := o.Batch(ctx, keys)
	if err != nil {
		return nil, err
	}
	statuses := make([]ports.OfferStatus, 0, len(states))
	for _, state := range states {
		p := state.Product.State()
		status := ports.OfferStatus{
			Key:         ids.OfferID{Tenant: p.TenantID, Product: p.ProductID},
			Revision:    p.Revision,
			Deleted:     p.Deleted,
			Publication: p.Card.Publication,
			Verdict:     state.Verdict,
		}
		latest, err := (moderationDecisions{o.d.moderation}).Latest(ctx, p.TenantID, []ids.ProductID{p.ProductID})
		if err != nil {
			return nil, err
		}
		if d, ok := latest[p.ProductID]; ok {
			status.Decision = &d
			status.CaseReasons = o.d.moderation.cases[d.CaseID].Reasons
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}
