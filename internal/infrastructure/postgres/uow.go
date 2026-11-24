package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// UnitOfWork — транзакция PostgreSQL на каждый вызов Do.
type UnitOfWork struct {
	source    *connectionSource
	snapshots snapshots
}

type scope struct {
	afterCommit []func()
	db          *gorm.DB
	snapshots   *snapshots
	taxonomy    *Taxonomy
	moderation  *ModerationRules
}

// NewUnitOfWork — UnitOfWork поверх одного пула с общими снимками всех его транзакций:
// CLI и тесты, где классы нагрузки не разделяются.
func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork {
	return &UnitOfWork{source: newConnectionSource(Single(pool))}
}

// NewUnitOfWorkByClass — UnitOfWork процесса: транзакция берёт соединение и сроки
// пула класса нагрузки из контекста.
func NewUnitOfWorkByClass(pools *Pools) *UnitOfWork {
	return &UnitOfWork{source: &connectionSource{pools: pools}}
}

func newScope(db *gorm.DB, snapshots *snapshots) *scope {
	return &scope{
		db:        db,
		snapshots: snapshots,
	}
}

// Do открывает транзакцию, выполняет fn и фиксирует; ошибка или паника fn откатывают.
func (u *UnitOfWork) Do(ctx context.Context, fn func(ctx context.Context, tx ports.Tx) error) (err error) {
	pool := u.source.pools.route(ctx)
	ctx, cancel := context.WithTimeout(ctx, pool.budget.Transaction)
	defer cancel()
	shared, err := u.source.begin(ctx, pool)
	if err != nil {
		return ErrUnavailable.Wrap(err)
	}
	defer shared.close()

	tx := shared.orm

	defer func() {
		if err != nil {
			_ = tx.Rollback().Error
		}
	}()

	defer func() {
		if recovered := recover(); recovered != nil {
			_ = tx.Rollback().Error
			panic(recovered)
		}
	}()

	scope := newScope(shared.orm, &u.snapshots)
	if err = fn(ctx, scope); err != nil {
		return err
	}

	if err = ctx.Err(); err != nil {
		return err
	}

	if commitErr := tx.Commit().Error; commitErr != nil {
		switch {
		case errors.Is(commitErr, pgx.ErrTxCommitRollback):
			// сценарий проглотил ошибку запроса, и база уже прервала транзакцию
			return fmt.Errorf("транзакция прервана базой и откатилась: %w", commitErr)
		case errors.Is(commitErr, context.Canceled) || errors.Is(commitErr, context.DeadlineExceeded):
			return commitErr
		case errors.Is(commitErr, sql.ErrTxDone) && ctx.Err() != nil:
			// database/sql мог откатить отменённую транзакцию между проверкой и Commit.
			return ctx.Err()
		default:
			return ErrUnavailable.Wrap(commitErr)
		}
	}
	// Наблюдатели могут открыть следующую транзакцию даже при пуле из одного соединения.
	shared.close()

	for _, observe := range scope.afterCommit {
		observe()
	}
	return nil
}

func (s *scope) AfterCommit(fn func()) { s.afterCommit = append(s.afterCommit, fn) }

func (s *scope) Participants() participant.Repository { return &Participants{db: s.db} }

func (s *scope) Registry() participant.Registry { return &Registry{db: s.db} }

func (s *scope) Products() catalog.ProductRepository { return &Products{db: s.db} }

func (s *scope) Signals() catalog.SignalsRepository { return &Signals{db: s.db} }

func (s *scope) StoreCards() ports.StoreCards { return &StoreCards{db: s.db} }

func (s *scope) Offers() ports.OfferStates { return &Offers{db: s.db} }

func (s *scope) Outbox() ports.Outbox { return &Outbox{db: s.db} }

func (s *scope) Inbox() ports.Inbox { return &Inbox{db: s.db} }

func (s *scope) Parked() ports.ParkedEvents { return &Parked{db: s.db} }

func (s *scope) Buyers() buyer.Repository { return &Buyers{db: s.db} }

func (s *scope) Challenges() buyer.ChallengeRepository { return &Challenges{db: s.db} }

func (s *scope) Carts() cart.Repository { return &Carts{db: s.db} }

func (s *scope) Checkouts() checkout.Repository { return &Checkouts{db: s.db} }

func (s *scope) Taxonomy() taxonomy.Repository {
	if s.taxonomy == nil {
		s.taxonomy = newTaxonomy(s.db, &s.snapshots.taxonomy, s.AfterCommit)
	}
	return s.taxonomy
}

func (s *scope) Reference() reference.Repository { return &References{db: s.db} }

func (s *scope) ModerationVerdicts() moderation.VerdictRepository {
	return &ModerationVerdicts{db: s.db}
}

func (s *scope) ProjectionValidation() ports.ProjectionValidation {
	return &ProjectionValidation{db: s.db}
}

func (s *scope) ModerationCases() moderation.CaseRepository { return &ModerationCases{db: s.db} }

func (s *scope) ModerationDecisions() moderation.DecisionRepository {
	return &ModerationDecisions{db: s.db}
}

func (s *scope) ModerationRules() moderation.RulesRepository {
	if s.moderation == nil {
		s.moderation = newModerationRules(s.db, &s.snapshots.moderation, s.AfterCommit)
	}
	return s.moderation
}

func (s *scope) ParticipantDecisions() participant.DecisionRepository {
	return &ParticipantDecisions{db: s.db}
}

func (s *scope) Promotion() promotion.Repository { return &Promotions{db: s.db} }

func (s *scope) Revalidation() ports.RevalidationStore { return &Revalidations{db: s.db} }
