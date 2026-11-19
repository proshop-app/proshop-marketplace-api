package moderationstatus

import (
	"context"
	"reflect"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type offerFeedback struct {
	decisions map[ids.ProductID]moderation.Decision
	reasons   map[ids.UUID][]moderation.Reason
}

func offerEvents(
	ctx context.Context,
	tx ports.Tx,
	tenant ids.TenantID,
	products []ids.ProductID,
	at string,
) ([]*OfferEvent, error) {
	keys := make([]ids.OfferID, 0, len(products))
	for _, product := range products {
		keys = append(keys, ids.OfferID{Tenant: tenant, Product: product})
	}
	states, err := tx.Offers().Batch(ctx, keys)
	if err != nil {
		return nil, err
	}
	decisions, err := offerDecisions(ctx, tx, tenant, states)
	if err != nil {
		return nil, err
	}
	reasons, err := rejectedCaseReasons(ctx, tx, decisions)
	if err != nil {
		return nil, err
	}
	feedback := offerFeedback{decisions: decisions, reasons: reasons}
	events := []*OfferEvent{}
	for _, state := range states {
		if !state.Product.State().Deleted {
			events = append(events, offerEvent(tenant, state, feedback, at))
		}
	}
	return events, nil
}

func offerEvent(tenant ids.TenantID, state ports.OfferState, feedback offerFeedback, at string) *OfferEvent {
	product := state.Product.State()
	status, reasons := Status(product.Card.Publication, state.Verdict)
	var decision *OfferDecision
	if d, ok := feedback.decisions[product.ProductID]; ok {
		decision = &OfferDecision{
			Action: d.Action,
			Reason: moderation.DecisionReason(d.Reason, d.ReasonMessage),
			Note:   d.Note,
			At:     d.At.UTC().Format(time.RFC3339),
		}
		if product.Card.Publication.Published && d.Action == moderation.Reject {
			reasons = rejectionReasons(reasons, feedback.reasons[d.CaseID])
		}
	}
	return &OfferEvent{
		Decision:      decision,
		SchemaVersion: 1,
		TenantID:      tenant,
		ProductID:     product.ProductID,
		Revision:      product.Revision,
		Status:        status,
		Visible:       status == "published",
		Reasons:       reasons,
		At:            at,
	}
}

func offerDecisions(
	ctx context.Context,
	tx ports.Tx,
	tenant ids.TenantID,
	states []ports.OfferState,
) (map[ids.ProductID]moderation.Decision, error) {
	products := []ids.ProductID{}
	for _, state := range states {
		if decisionRequired(state) {
			products = append(products, state.Product.State().ProductID)
		}
	}
	if len(products) == 0 {
		return nil, nil
	}
	latest, err := tx.ModerationDecisions().Latest(ctx, tenant, products)
	if err != nil {
		return nil, err
	}
	result := make(map[ids.ProductID]moderation.Decision)
	for _, state := range states {
		product := state.Product.State()
		if d, ok := latest[product.ProductID]; ok && decisionMatches(state, d) {
			result[product.ProductID] = d
		}
	}
	return result, nil
}

func decisionRequired(state ports.OfferState) bool {
	product := state.Product.State()
	if product.Deleted || state.Verdict == nil {
		return false
	}
	v := state.Verdict.State()
	return v.DecidedBy == moderation.Operator && v.Status != moderation.Pending &&
		v.Revision == product.Revision && v.Decision != nil
}

func decisionMatches(state ports.OfferState, d moderation.Decision) bool {
	return verdictDecisionMatches(state.Verdict, state.Product.State().Revision, d)
}

func verdictDecisionMatches(verdict *moderation.Verdict, revision int64, d moderation.Decision) bool {
	v := verdict.State()
	return d.Revision <= revision && d.Action == v.Decision.Action &&
		d.Operator == v.Operator && d.At.Equal(v.Decision.At) && d.Note == v.Decision.Note &&
		reflect.DeepEqual(moderation.DecisionReason(d.Reason, d.ReasonMessage), v.Decision.Reason)
}

func rejectedCaseReasons(
	ctx context.Context,
	tx ports.Tx,
	decisions map[ids.ProductID]moderation.Decision,
) (map[ids.UUID][]moderation.Reason, error) {
	cases := []ids.UUID{}
	for _, d := range decisions {
		if d.Action == moderation.Reject {
			cases = append(cases, d.CaseID)
		}
	}
	if len(cases) == 0 {
		return nil, nil
	}
	reasons, err := tx.ModerationCases().Reasons(ctx, cases)
	if err != nil {
		return nil, err
	}
	for _, id := range cases {
		if _, ok := reasons[id]; !ok {
			return nil, moderation.ErrNotFound
		}
	}
	return reasons, nil
}
