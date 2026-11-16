// Package moderation выполняет решения оператора и автоматический допуск предложений.
package moderation

import (
	"context"
	"errors"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

// Config передаёт политику допуска без зависимости от окружения.
type Config struct {
	Metrics         ports.ModerationMetrics
	ProbationOffers int
	RequireImage    bool
}

// Service связывает вердикт, журнал и outbox одной транзакцией.
type Service struct {
	uow    ports.UnitOfWork
	clock  ports.Clock
	ids    ports.IDs
	config Config
}

// NewService получает внешние зависимости через порты.
func NewService(uow ports.UnitOfWork, clock ports.Clock, identities ports.IDs, config Config) *Service {
	return &Service{uow: uow, clock: clock, ids: identities, config: config}
}

// Evaluate сохраняет порядок правил и защищает решение оператора текущей ревизии.
func (s *Service) Evaluate(
	ctx context.Context,
	tx ports.Tx,
	product *catalog.Product,
	member *participant.Participant,
	previous *catalog.ProductState,
	now time.Time,
) error {
	changed, err := s.evaluate(ctx, tx, product, member, previous, now)
	if err != nil || !changed {
		return err
	}
	return s.enqueueEvaluation(ctx, tx, product.State(), now)
}

// EvaluateProjection оставляет постановку задач вызывающему проектору.
func (s *Service) EvaluateProjection(
	ctx context.Context,
	tx ports.Tx,
	product *catalog.Product,
	previous *catalog.ProductState,
	now time.Time,
) error {
	_, err := s.evaluate(ctx, tx, product, nil, previous, now)
	return err
}

func (s *Service) evaluate(
	ctx context.Context,
	tx ports.Tx,
	product *catalog.Product,
	member *participant.Participant,
	previous *catalog.ProductState,
	now time.Time,
) (bool, error) {
	p := product.State()
	if p.Deleted || p.Card.Marketplace == nil || !p.Card.Publication.Published {
		return s.closeUnpublishedCase(ctx, tx, p, now)
	}
	if member == nil {
		var err error
		member, err = tx.Participants().Get(ctx, p.TenantID)
		if errors.Is(err, participant.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	definition, err := tx.Taxonomy().Current(ctx)
	if err != nil {
		return false, err
	}
	rules, err := tx.ModerationRules().Current(ctx)
	if err != nil {
		return false, err
	}
	rules.RequireImage = s.config.RequireImage
	verdict, err := tx.ModerationVerdicts().Get(ctx, p.TenantID, p.ProductID)
	if errors.Is(err, moderation.ErrNotFound) {
		verdict, err = moderation.NewVerdict(p.TenantID, p.ProductID), nil
	}
	if err != nil {
		return false, err
	}
	input := moderation.Input{
		Policy:      definition.ModerationPolicy(p.Card.Marketplace.CategoryID),
		Name:        p.Card.Name,
		Description: p.Card.Description,
		Image:       p.Card.Image,
		Probation:   member.State().Trust == participant.Probation,
	}
	changed := false
	if previous != nil && previous.Revision == verdict.State().Revision &&
		catalog.IdenticalProjectionContent(*previous, p) {
		changed, err = s.carryReviewedProjection(
			ctx,
			tx,
			verdict,
			verdict.State(),
			p.Revision,
			definition,
			rules,
			input,
			now,
		)
		if err != nil {
			return false, err
		}
	}
	if !changed {
		changed = verdict.Evaluate(p.Revision, definition.Version, rules, input, now)
	}
	if !changed {
		if verdict.State().Status != moderation.Pending {
			return false, nil
		}
		return s.syncEvaluationCase(ctx, tx, p, verdict, false, now)
	}
	return s.saveEvaluation(ctx, tx, p, verdict, now)
}

func (s *Service) saveEvaluation(
	ctx context.Context,
	tx ports.Tx,
	p catalog.ProductState,
	verdict *moderation.Verdict,
	now time.Time,
) (bool, error) {
	if err := tx.ModerationVerdicts().Save(ctx, verdict); err != nil {
		return false, err
	}
	s.recordVerdict(tx, verdict.State())
	return s.syncEvaluationCase(ctx, tx, p, verdict, true, now)
}

func (s *Service) syncEvaluationCase(
	ctx context.Context,
	tx ports.Tx,
	p catalog.ProductState,
	verdict *moderation.Verdict,
	verdictChanged bool,
	now time.Time,
) (bool, error) {
	c, err := tx.ModerationCases().Open(ctx, p.TenantID, p.ProductID)
	if err != nil && !errors.Is(err, moderation.ErrNotFound) {
		return false, err
	}
	var card *moderation.ReviewCard
	// Снимок нужен только новому делу или новой ревизии существующего дела.
	if (c == nil && verdict.State().Status == moderation.Pending) ||
		(c != nil && c.State().Revision != verdict.State().Revision) {
		snapshot, err := reviewCard(ctx, tx, p.TenantID, p.ProductID)
		if err != nil {
			return false, err
		}
		card = &snapshot
	}
	switch {
	case c != nil:
		if c.Refresh(verdict.State(), now, card) {
			if err := tx.ModerationCases().Save(ctx, c); err != nil {
				return false, err
			}
		} else if !verdictChanged {
			return false, nil
		}
	case verdict.State().Status == moderation.Pending:
		c = moderation.NewCase(s.ids.NewUUID(), verdict.State(), moderation.Rule, "", now, card)
		if err := tx.ModerationCases().Save(ctx, c); err != nil {
			return false, err
		}
	case !verdictChanged:
		return false, nil
	}
	return true, nil
}

func (s *Service) closeUnpublishedCase(
	ctx context.Context,
	tx ports.Tx,
	p catalog.ProductState,
	now time.Time,
) (bool, error) {
	c, err := tx.ModerationCases().Open(ctx, p.TenantID, p.ProductID)
	if errors.Is(err, moderation.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Вердикт сохраняем: временное снятие публикации не отменяет решение оператора.
	c.Supersede(now)
	if err := tx.ModerationCases().Save(ctx, c); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) enqueueEvaluation(
	ctx context.Context,
	tx ports.Tx,
	p catalog.ProductState,
	now time.Time,
) error {
	if err := tx.Outbox().Enqueue(ctx, moderationstatus.OfferTask(p.TenantID, p.ProductID, now)); err != nil {
		return err
	}
	// Пересчёт сводки магазина ставит синхронизация предложения — с окном склейки.
	return tx.Outbox().Enqueue(ctx, appcatalog.SyncOfferTask(p.TenantID, p.ProductID, now))
}

func (s *Service) recordVerdict(tx ports.Tx, state moderation.VerdictState) {
	if s.config.Metrics != nil {
		tx.AfterCommit(func() { s.config.Metrics.Verdict(state) })
	}
}
