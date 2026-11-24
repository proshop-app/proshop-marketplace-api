//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

// classRolePassword — пароль одноразовых ролей теста, только для локального compose.
//
//nolint:gosec // не секрет: роли живут на время теста
const classRolePassword = "class-role-test"

// classRoles заводит роль на класс со сроками ALTER ROLE … SET — так их задаёт развёртывание
// за пулером — и удаляет роли после теста.
func classRoles(
	t *testing.T,
	admin *pgxpool.Pool,
	budgets map[ports.DatabaseClass]postgres.Budget,
) map[ports.DatabaseClass]postgres.PoolConfig {
	t.Helper()
	ctx := context.Background()
	configs := make(map[ports.DatabaseClass]postgres.PoolConfig, len(budgets))
	for class, budget := range budgets {
		role := fmt.Sprintf("class_%d_%d", class, time.Now().UnixNano())
		statements := []string{
			fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", role, classRolePassword),
			fmt.Sprintf("ALTER ROLE %s SET statement_timeout = '%dms'", role, budget.Statement.Milliseconds()),
			fmt.Sprintf(
				"ALTER ROLE %s SET idle_in_transaction_session_timeout = '%dms'",
				role,
				budget.Transaction.Milliseconds(),
			),
		}
		for _, statement := range statements {
			if _, err := admin.Exec(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() {
			if _, err := admin.Exec(context.Background(), "DROP ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
				t.Errorf("удаление роли: %v", err)
			}
		})
		configs[class] = postgres.PoolConfig{MaxConns: 1, Budget: budget, User: role, Password: classRolePassword}
	}
	return configs
}

// withoutCredentials — адрес базы без учётных данных: за пулером их несёт роль класса.
func withoutCredentials(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = nil
	return parsed.String()
}

func TestPooledClassesTakeBudgetFromRole(t *testing.T) {
	database := testPool(t)
	ctx := context.Background()
	fast := postgres.Budget{Statement: 75 * time.Millisecond, Transaction: 300 * time.Millisecond}
	slow := postgres.Budget{Statement: 10 * time.Second, Transaction: 30 * time.Second}
	configs := classRoles(t, database, map[ports.DatabaseClass]postgres.Budget{
		ports.DatabaseRead:       fast,
		ports.DatabaseCheckout:   slow,
		ports.DatabaseIngestion:  slow,
		ports.DatabaseBackground: slow,
		ports.DatabaseOrders:     slow,
	})
	target := postgres.Target{URL: withoutCredentials(t, database.Config().ConnString()), Pooled: true}
	pools, err := postgres.OpenPools(ctx, target, configs)
	if err != nil {
		t.Fatal(err)
	}
	defer pools.Close()
	read := pools.Pool(ports.DatabaseRead)
	if _, ok := read.Config().ConnConfig.RuntimeParams["statement_timeout"]; ok {
		t.Fatal("через пулер сроки не передаются параметрами запуска")
	}
	var statement string
	if err := read.QueryRow(ctx, `SHOW statement_timeout`).Scan(&statement); err != nil || statement != "75ms" {
		t.Fatalf("срок класса задан ролью: %s, %v", statement, err)
	}
	err = postgres.NewUnitOfWorkByClass(pools).Do(
		ports.WithDatabaseClass(ctx, ports.DatabaseRead),
		func(ctx context.Context, _ ports.Tx) error {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > fast.Transaction {
				return fmt.Errorf("срок транзакции класса не из настройки: %v", deadline)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestPooledClassWithForeignRoleBudgetDoesNotStart(t *testing.T) {
	database := testPool(t)
	budget := postgres.Budget{Statement: 2 * time.Second, Transaction: 5 * time.Second}
	configs := classRoles(t, database, map[ports.DatabaseClass]postgres.Budget{
		ports.DatabaseRead:       budget,
		ports.DatabaseCheckout:   budget,
		ports.DatabaseIngestion:  budget,
		ports.DatabaseBackground: budget,
		ports.DatabaseOrders:     budget,
	})
	read := configs[ports.DatabaseRead]
	read.Budget = postgres.Budget{Statement: 750 * time.Millisecond, Transaction: 2 * time.Second}
	configs[ports.DatabaseRead] = read
	target := postgres.Target{URL: withoutCredentials(t, database.Config().ConnString()), Pooled: true}
	pools, err := postgres.OpenPools(context.Background(), target, configs)
	if err == nil {
		pools.Close()
		t.Fatal("роль со сроками другого класса должна остановить старт")
	}
	if !strings.Contains(err.Error(), "statement_timeout 2000ms") {
		t.Fatalf("ошибка называет срок роли: %v", err)
	}
}
