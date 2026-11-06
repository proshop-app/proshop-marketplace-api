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
	now time.Time,
) error {
	p := product.State()
	if p.Deleted || p.Card.Marketplace == nil {
		return nil
	}
	if member == nil {
		var err error
		member, err = tx.Participants().Get(ctx, p.TenantID)
		if errors.Is(err, participant.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	definition, err := tx.Taxonomy().Current(ctx)
	if err != nil {
		return err
	}
	rules, err := tx.ModerationRules().Current(ctx)
	if err != nil {
		return err
	}
	rules.RequireImage = s.config.RequireImage
	verdict, err := tx.ModerationVerdicts().Get(ctx, p.TenantID, p.ProductID)
	if errors.Is(err, moderation.ErrNotFound) {
		verdict, err = moderation.NewVerdict(p.TenantID, p.ProductID), nil
	}
	if err != nil {
		return err
	}
	input := moderation.Input{
		Policy:      definition.ModerationPolicy(p.Card.Marketplace.CategoryID),
		Name:        p.Card.Name,
		Description: p.Card.Description,
		Image:       p.Card.Image,
		Probation:   member.State().Trust == participant.Probation,
	}
	changed := verdict.Evaluate(p.Revision, definition.Version, rules, input, now)
	if !changed {
		return nil
	}
	if err := tx.ModerationVerdicts().Save(ctx, verdict); err != nil {
		return err
	}
	s.recordVerdict(tx, verdict.State())
	c, err := tx.ModerationCases().Open(ctx, p.TenantID, p.ProductID)
	if err != nil && !errors.Is(err, moderation.ErrNotFound) {
		return err
	}
	card, err := reviewCard(ctx, tx, p.TenantID, p.ProductID)
	if err != nil {
		return err
	}
	if c != nil {
		if c.Refresh(verdict.State(), now, &card) {
			if err := tx.ModerationCases().Save(ctx, c); err != nil {
				return err
			}
		}
	} else if verdict.State().Status == moderation.Pending {
		c = moderation.NewCase(s.ids.NewUUID(), verdict.State(), moderation.Rule, "", now, &card)
		if err := tx.ModerationCases().Save(ctx, c); err != nil {
			return err
		}
	}
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
