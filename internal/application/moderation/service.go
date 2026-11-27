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

// Evaluate сохраняет вердикт и задачи после отдельного пересмотра предложения.
func (s *Service) Evaluate(
	ctx context.Context,
	tx ports.Tx,
	product *catalog.Product,
	member *participant.Participant,
	previous *catalog.ProductState,
	now time.Time,
) error {
	decision, err := s.evaluate(ctx, tx, product, member, previous, now)
	if err != nil || !decision.Changed {
		return err
	}
	state := product.State()
	return tx.Outbox().EnqueueBatch(ctx, evaluationTasks(state, now))
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
) (evaluationDecision, error) {
	input, err := s.readEvaluation(ctx, tx, product, member, previous, now)
	if err != nil {
		return evaluationDecision{}, err
	}
	decision := decideVerdict(input)
	if err := readEvaluationCase(ctx, tx, &input, decision); err != nil {
		return evaluationDecision{}, err
	}
	decision = s.completeEvaluation(input, decision)
	return decision, saveDecision(ctx, tx, decision)
}

func evaluationTasks(p catalog.ProductState, now time.Time) []ports.NewTask {
	return []ports.NewTask{
		moderationstatus.OfferTask(p.TenantID, p.ProductID, now),
		appcatalog.SyncOfferTask(p.TenantID, p.ProductID, now),
	}
}

func (s *Service) recordVerdict(tx ports.Tx, state moderation.VerdictState) {
	if s.config.Metrics != nil {
		tx.AfterCommit(func() { s.config.Metrics.Verdict(state) })
	}
}
