package postgres

import (
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultStatementTimeout   = 10 * time.Second
	defaultTransactionTimeout = 30 * time.Second
)

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
	return strconv.FormatInt(max(1, value.Milliseconds()), 10) + "ms"
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
