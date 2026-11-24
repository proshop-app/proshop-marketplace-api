package postgres

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultStatementTimeout   = 10 * time.Second
	defaultTransactionTimeout = 30 * time.Second
)

// selectRoleBudget — сроки, которые сервер применил к соединению: через пулер их задаёт
// роль класса. pg_settings отдаёт оба срока целыми миллисекундами.
const selectRoleBudget = `SELECT current_user,
    (SELECT setting::bigint FROM pg_settings WHERE name = 'statement_timeout'),
    (SELECT setting::bigint FROM pg_settings WHERE name = 'idle_in_transaction_session_timeout')`

// Budget — срок SQL и всей операции хранения, включая ожидание соединения.
type Budget struct {
	Statement   time.Duration
	Transaction time.Duration
}

// applyBudget задаёт серверные сроки при открытии соединения. У каждого класса
// нагрузки свой пул, поэтому заём соединения не переключает настройки отдельным SET.
func applyBudget(cfg *pgxpool.Config, budget Budget) {
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = databaseDuration(budget.Statement)
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = databaseDuration(budget.Transaction)
}

func databaseDuration(value time.Duration) string {
	return strconv.FormatInt(databaseMilliseconds(value), 10) + "ms"
}

// databaseMilliseconds — срок в единицах сервера; ноль у сервера значит «без срока».
func databaseMilliseconds(value time.Duration) int64 {
	return max(1, value.Milliseconds())
}

// verifyRoleBudget сверяет сроки роли класса с настройкой процесса: расхождение —
// отказ старта, а не тихая работа без срока или с чужим сроком.
func verifyRoleBudget(ctx context.Context, pool *pgxpool.Pool, budget Budget) error {
	var role string
	var statement, idle int64
	if err := pool.QueryRow(ctx, selectRoleBudget).Scan(&role, &statement, &idle); err != nil {
		return ErrUnavailable.Wrap(err)
	}
	expected := databaseMilliseconds(budget.Statement)
	expectedIdle := databaseMilliseconds(budget.Transaction)
	if statement == expected && idle == expectedIdle {
		return nil
	}
	const mismatch = "роль %s: statement_timeout %dms и idle_in_transaction_session_timeout %dms, " +
		"ожидаются %dms и %dms (ALTER ROLE … SET и RECONNECT пулера)"
	return fmt.Errorf(mismatch, role, statement, idle, expected, expectedIdle)
}

// poolBudget восстанавливает сроки пула, открытого не через OpenPools: CLI и тесты.
func poolBudget(pool *pgxpool.Pool) Budget {
	params := pool.Config().ConnConfig.RuntimeParams
	statement, _ := time.ParseDuration(params["statement_timeout"])
	transaction, _ := time.ParseDuration(params["idle_in_transaction_session_timeout"])
	return defaultBudget(Budget{Statement: statement, Transaction: transaction})
}

func defaultBudget(budget Budget) Budget {
	if budget.Statement <= 0 {
		budget.Statement = defaultStatementTimeout
	}
	if budget.Transaction <= 0 {
		budget.Transaction = defaultTransactionTimeout
	}
	return budget
}

func validateBudget(budget Budget) error {
	if budget.Statement <= 0 || budget.Transaction < budget.Statement {
		return fmt.Errorf("срок SQL должен быть положительным и не превышать срок транзакции")
	}
	return nil
}
