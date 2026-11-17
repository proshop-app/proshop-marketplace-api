package moderation

import (
	"context"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func (s *Service) readEvaluation(
	ctx context.Context,
	tx ports.Tx,
	product *catalog.Product,
	member *participant.Participant,
	previous *catalog.ProductState,
	now time.Time,
) (evaluationInput, error) {
	in := evaluationInput{Product: product, Participant: member, Now: now}
	if !evaluationPublished(in) {
		return in, nil
	}
	p := product.State()
	var err error
	if member == nil {
		in.Participant, err = tx.Participants().Get(ctx, p.TenantID)
		if errors.Is(err, participant.ErrNotFound) {
			return in, nil
		}
		if err != nil {
			return in, err
		}
	}
	in.Definition, err = tx.Taxonomy().Current(ctx)
	if err != nil {
		return in, err
	}
	in.Policy = in.Definition.ModerationPolicy(p.Card.Marketplace.CategoryID)
	in.Rules, err = tx.ModerationRules().Current(ctx)
	if err != nil {
		return in, err
	}
	in.Rules.RequireImage = s.config.RequireImage
	in.Verdict, err = tx.ModerationVerdicts().Get(ctx, p.TenantID, p.ProductID)
	if errors.Is(err, moderation.ErrNotFound) {
		err = nil
	}
	if err != nil || in.Verdict == nil || previous == nil || previous.Revision != in.Verdict.State().Revision ||
		!catalog.IdenticalProjectionContent(*previous, p) {
		return in, err
	}
	approved := in.Verdict.State()
	allowed, err := s.reviewedProjectionProof(ctx, tx, approved, in.Definition, in.Rules, evaluationRulesInput(in))
	if allowed {
		in.Approval = &approved
	}
	return in, err
}

func readEvaluationCase(ctx context.Context, tx ports.Tx, in *evaluationInput, decision evaluationDecision) error {
	if !evaluationNeedsCase(*in, decision) {
		return nil
	}
	p := in.Product.State()
	var err error
	in.Case, err = tx.ModerationCases().Open(ctx, p.TenantID, p.ProductID)
	if err != nil && !errors.Is(err, moderation.ErrNotFound) {
		return err
	}
	if !evaluationNeedsReview(*in, decision) {
		return nil
	}
	in.Signals, err = tx.Signals().Get(ctx, p.TenantID, p.ProductID)
	if errors.Is(err, catalog.ErrSignalsNotFound) {
		return nil
	}
	return err
}

func saveDecision(ctx context.Context, tx ports.Tx, decision evaluationDecision) error {
	if decision.Verdict != nil {
		if err := tx.ModerationVerdicts().Save(ctx, decision.Verdict); err != nil {
			return err
		}
	}
	if decision.Case != nil {
		if err := tx.ModerationCases().Save(ctx, decision.Case); err != nil {
			return err
		}
	}
	for _, observe := range decision.Observations {
		tx.AfterCommit(observe)
	}
	return nil
}

// saveEvaluation сохраняет уже подтверждённый RecoverProjection допуск через то же решение дела.
func (s *Service) saveEvaluation(
	ctx context.Context,
	tx ports.Tx,
	p catalog.ProductState,
	member *participant.Participant,
	verdict *moderation.Verdict,
	definition taxonomy.Definition,
	now time.Time,
) (bool, error) {
	in := evaluationInput{Product: catalog.RestoreProduct(p), Definition: definition, Verdict: verdict, Now: now}
	in.Participant = member
	decision := evaluationDecision{Verdict: verdict, Changed: true}
	if err := readEvaluationCase(ctx, tx, &in, decision); err != nil {
		return false, err
	}
	decision = s.completeEvaluation(in, decision)
	return decision.Changed, saveDecision(ctx, tx, decision)
}
