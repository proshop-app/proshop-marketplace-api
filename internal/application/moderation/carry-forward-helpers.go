package moderation

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func (s *Service) carryReviewedProjection(
	ctx context.Context,
	tx ports.Tx,
	verdict *moderation.Verdict,
	approved moderation.VerdictState,
	revision int64,
	definition taxonomy.Definition,
	rules moderation.Rules,
	input moderation.Input,
	now time.Time,
) (bool, error) {
	if approved.DecidedBy != moderation.Operator || approved.Status != moderation.Allowed ||
		approved.Decision == nil || approved.Decision.Action != moderation.Allow || approved.Operator.ID == "" ||
		approved.ManualReject || approved.RulesRevision != rules.Revision {
		return false, nil
	}
	status, reasons := rules.Evaluate(input)
	if status != moderation.Pending || !probationOnly(reasons) {
		return false, nil
	}
	decisions, err := tx.ModerationDecisions().List(ctx, approved.TenantID, approved.ProductID)
	if err != nil {
		return false, err
	}
	if len(decisions) == 0 {
		return false, nil
	}
	decision := decisions[0]
	reason := moderation.DecisionReason(decision.Reason, decision.ReasonMessage)
	if decision.Action != moderation.Allow || decision.Revision < 1 || decision.Revision > approved.Revision ||
		decision.Operator != approved.Operator || !decision.At.Equal(approved.Decision.At) ||
		decision.Note != approved.Decision.Note || !reflect.DeepEqual(reason, approved.Decision.Reason) {
		return false, nil
	}
	c, err := tx.ModerationCases().Get(ctx, decision.CaseID)
	if err != nil {
		return false, err
	}
	closed := c.State()
	if closed.Status != moderation.Allowed || closed.Revision != decision.Revision || closed.Card == nil ||
		closed.TenantID != approved.TenantID || closed.ProductID != approved.ProductID ||
		closed.DecidedAt == nil || !closed.DecidedAt.Equal(decision.At) || !reflect.DeepEqual(closed.Reasons, reasons) {
		return false, nil
	}
	previousDefinition, err := tx.Taxonomy().Version(ctx, approved.TaxonomyVersion)
	if errors.Is(err, taxonomy.ErrInvalid) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	category := closed.Card.CategoryID
	if _, _, found := previousDefinition.Category(category); !found {
		return false, nil
	}
	if _, _, found := definition.Category(category); !found {
		return false, nil
	}
	if previousDefinition.ModerationPolicy(category) != definition.ModerationPolicy(category) {
		return false, nil
	}
	current, err := reviewCard(ctx, tx, approved.TenantID, approved.ProductID)
	if err != nil {
		return false, err
	}
	if !sameSemanticReviewCard(*closed.Card, current) || !current.Publication.Published {
		return false, nil
	}
	return verdict.CarryForward(approved, revision, definition.Version, now), nil
}

func sameSemanticReviewCard(previous, current moderation.ReviewCard) bool {
	previous.Publication.Version, current.Publication.Version = 0, 0
	previous.Publication.AttributeSearchTerms, current.Publication.AttributeSearchTerms = nil, nil
	return reflect.DeepEqual(previous, current)
}

func probationOnly(reasons []moderation.Reason) bool {
	return len(reasons) == 1 && reasons[0].Key == "moderation.probation"
}
