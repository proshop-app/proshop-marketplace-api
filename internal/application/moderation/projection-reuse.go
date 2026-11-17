package moderation

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

// PrepareReuse читает только номера источников и прежнюю оценку; ручные решения идут прежним путём.
func (s *Service) PrepareReuse(
	ctx context.Context,
	tx ports.Tx,
	previous catalog.ProductState,
) (*ports.ProjectionReuse, error) {
	sources, err := tx.ProjectionValidation().Current(ctx, previous.TenantID, previous.ProductID)
	if err != nil || !reusableProjection(previous, sources, s.config.RequireImage) {
		return nil, err
	}
	reuse := &ports.ProjectionReuse{Verdict: sources.Verdict}
	if sources.Verdict.State().Status != moderation.Pending {
		return reuse, nil
	}
	c, err := tx.ModerationCases().Open(ctx, previous.TenantID, previous.ProductID)
	if errors.Is(err, moderation.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	state := c.State()
	if state.Revision != previous.Revision || state.Card == nil || state.Source != moderation.Rule {
		return nil, nil
	}
	reuse.Case = c
	return reuse, nil
}

func reusableProjection(previous catalog.ProductState, sources ports.ValidationSources, requireImage bool) bool {
	if sources.Verdict == nil || !sources.Participant || sources.TaxonomyVersion < 1 {
		return false
	}
	v := sources.Verdict.State()
	if v.DecidedBy != moderation.Rule || v.ManualReject || v.Revision != previous.Revision ||
		v.TaxonomyVersion != sources.TaxonomyVersion || v.RulesRevision != sources.RulesRevision ||
		previous.Card.Publication.Version != sources.TaxonomyVersion {
		return false
	}
	hasReason := func(key string) bool {
		return slices.ContainsFunc(v.Reasons, func(reason moderation.Reason) bool { return reason.Key == key })
	}
	if hasReason("moderation.category_forbidden") {
		return true
	}
	missingImage := requireImage && strings.TrimSpace(previous.Card.Image) == ""
	if missingImage != hasReason("moderation.image_missing") {
		return false
	}
	// Запрет категории и отсутствие обязательного изображения завершают Rules.Evaluate до доверия.
	return missingImage || sources.Probation == hasReason("moderation.probation")
}

// ReuseProjection обновляет только ревизии оценки и снимок уже открытого дела.
func (s *Service) ReuseProjection(
	ctx context.Context,
	tx ports.Tx,
	product *catalog.Product,
	reuse *ports.ProjectionReuse,
	now time.Time,
) error {
	if !reuse.Verdict.ReuseProjection(product.State().Revision, now) {
		return moderation.ErrConflict
	}
	if err := tx.ModerationVerdicts().Save(ctx, reuse.Verdict); err != nil {
		return err
	}
	s.recordVerdict(tx, reuse.Verdict.State())
	if reuse.Case == nil {
		return nil
	}
	p := product.State()
	state, err := tx.Offers().Offer(ctx, p.TenantID, p.ProductID)
	if err != nil {
		return err
	}
	card := snapshotReviewCard(state)
	card.CategoryPath = reuse.Case.State().Card.CategoryPath
	reuse.Case.Refresh(reuse.Verdict.State(), now, &card)
	return tx.ModerationCases().Save(ctx, reuse.Case)
}
