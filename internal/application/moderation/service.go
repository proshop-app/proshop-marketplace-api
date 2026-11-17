// Package moderation выполняет решения оператора и автоматический допуск предложений.
package moderation

import (
	"context"
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
	input, err := s.readEvaluation(ctx, tx, product, member, previous, now)
	if err != nil {
		return false, err
	}
	decision := decideVerdict(input)
	if err := readEvaluationCase(ctx, tx, &input, decision); err != nil {
		return false, err
	}
	decision = s.completeEvaluation(input, decision)
	return decision.Changed, saveDecision(ctx, tx, decision)
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
