package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// querier — общее у транзакции и пула.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	// SendBatch шлёт пачку операторов за один круг до базы, порядок сохраняется.
	SendBatch(ctx context.Context, batch *pgx.Batch) pgx.BatchResults
}

// UnitOfWork — транзакция PostgreSQL на каждый вызов Do.
type UnitOfWork struct {
	pool *pgxpool.Pool
}

type scope struct {
	afterCommit []func()
	q           querier
}

// NewUnitOfWork — UnitOfWork поверх пула.
func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork {
	return &UnitOfWork{pool: pool}
}

// Do открывает транзакцию, выполняет fn и фиксирует; ошибка или паника fn откатывают.
func (u *UnitOfWork) Do(ctx context.Context, fn func(ctx context.Context, tx ports.Tx) error) (err error) {
	tx, err := u.pool.Begin(ctx)
	if err != nil {
		return ErrUnavailable.Wrap(err)
	}

	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	defer func() {
		if recovered := recover(); recovered != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(recovered)
		}
	}()

	scope := &scope{q: tx}
	if err = fn(ctx, scope); err != nil {
		return err
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		switch {
		case errors.Is(commitErr, pgx.ErrTxCommitRollback):
			// сценарий проглотил ошибку запроса, и база уже прервала транзакцию
			return fmt.Errorf("транзакция прервана базой и откатилась: %w", commitErr)
		case errors.Is(commitErr, context.Canceled) || errors.Is(commitErr, context.DeadlineExceeded):
			return commitErr
		default:
			return ErrUnavailable.Wrap(commitErr)
		}
	}

	for _, observe := range scope.afterCommit {
		observe()
	}
	return nil
}

func (s *scope) AfterCommit(fn func()) { s.afterCommit = append(s.afterCommit, fn) }

func (s *scope) Participants() participant.Repository { return &Participants{q: s.q} }

func (s *scope) Registry() participant.Registry { return &Registry{q: s.q} }

func (s *scope) Products() catalog.ProductRepository { return &Products{q: s.q} }

func (s *scope) Signals() catalog.SignalsRepository { return &Signals{q: s.q} }

func (s *scope) StoreCards() ports.StoreCards { return &StoreCards{q: s.q} }

func (s *scope) Offers() ports.OfferStates { return &Offers{q: s.q} }

func (s *scope) Outbox() ports.Outbox { return &Outbox{q: s.q} }

func (s *scope) Inbox() ports.Inbox { return &Inbox{q: s.q} }

func (s *scope) Parked() ports.ParkedEvents { return &Parked{q: s.q} }

func (s *scope) Buyers() buyer.Repository { return &Buyers{q: s.q} }

func (s *scope) Challenges() buyer.ChallengeRepository { return &Challenges{q: s.q} }

func (s *scope) Carts() cart.Repository { return &Carts{q: s.q} }

func (s *scope) Checkouts() checkout.Repository { return &Checkouts{q: s.q} }

func (s *scope) PickupPoints() delivery.PickupPointRepository {
	return &PickupPoints{q: s.q}
}

func (s *scope) Taxonomy() taxonomy.Repository { return &Taxonomy{q: s.q} }

func (s *scope) Reference() reference.Repository { return &References{q: s.q} }

func (s *scope) ModerationVerdicts() moderation.VerdictRepository { return &ModerationVerdicts{q: s.q} }

func (s *scope) ModerationCases() moderation.CaseRepository { return &ModerationCases{q: s.q} }

func (s *scope) ModerationDecisions() moderation.DecisionRepository {
	return &ModerationDecisions{q: s.q}
}

func (s *scope) ModerationRules() moderation.RulesRepository { return &ModerationRules{q: s.q} }

func (s *scope) ParticipantDecisions() participant.DecisionRepository {
	return &ParticipantDecisions{q: s.q}
}

func (s *scope) Promotion() promotion.Repository { return &Promotions{q: s.q} }

func (s *scope) TaxonomyRevalidation() ports.TaxonomyRevalidation { return &Taxonomy{q: s.q} }
