package postgres

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const configureConnectionBudget = `SELECT set_config('statement_timeout', $1, false),
    set_config('idle_in_transaction_session_timeout', $2, false)`

// prepareBudgets меняет серверные сроки только при смене класса соединения.
// Один SET на смену класса дешевле SET LOCAL в каждой транзакции; следующий
// заём всегда проверяет настройки, в том числе для прямых вызовов пула.
func prepareBudgets(cfg *pgxpool.Config, fallback ports.DatabaseBudget) {
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = databaseDuration(fallback.Statement)
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = databaseDuration(fallback.Transaction)
	var applied sync.Map
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		applied.Store(conn, fallback)
		return nil
	}
	cfg.BeforeClose = func(conn *pgx.Conn) { applied.Delete(conn) }
	cfg.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		budget := ports.DatabaseBudgetFrom(ctx, fallback)
		previous, _ := applied.Load(conn)
		if previous == budget {
			return true, nil
		}
		_, err := conn.Exec(
			ctx,
			configureConnectionBudget,
			pgx.QueryExecModeExec,
			databaseDuration(budget.Statement),
			databaseDuration(budget.Transaction),
		)
		if err != nil {
			return false, err
		}
		applied.Store(conn, budget)
		return true, nil
	}
}

func databaseDuration(value time.Duration) string {
	return strconv.FormatInt(max(1, value.Milliseconds()), 10) + "ms"
}

func poolBudget(pool *pgxpool.Pool) ports.DatabaseBudget {
	params := pool.Config().ConnConfig.RuntimeParams
	statement, _ := time.ParseDuration(params["statement_timeout"])
	transaction, _ := time.ParseDuration(params["idle_in_transaction_session_timeout"])
	return defaultDatabaseBudget(ports.DatabaseBudget{Statement: statement, Transaction: transaction})
}

func defaultDatabaseBudget(budget ports.DatabaseBudget) ports.DatabaseBudget {
	if budget.Statement <= 0 {
		budget.Statement = 10 * time.Second
	}
	if budget.Transaction <= 0 {
		budget.Transaction = 30 * time.Second
	}
	return budget
}

func validateDatabaseBudget(budget ports.DatabaseBudget) error {
	if budget.Statement <= 0 || budget.Transaction < budget.Statement {
		return fmt.Errorf("срок SQL должен быть положительным и не превышать срок транзакции")
	}
	return nil
}
