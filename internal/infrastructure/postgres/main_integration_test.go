//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const defaultTestDatabaseURL = "postgres://marketplace:marketplace@localhost:25432/marketplace?sslmode=disable"

// testPool — отдельная база на тест с миграциями с нуля; удаляется после теста.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx := context.Background()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		base = defaultTestDatabaseURL
	}

	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatalf("база для тестов: %v", err)
	}
	t.Cleanup(admin.Close)

	name := fmt.Sprintf("marketplace_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("создание базы: %v", err)
	}

	parsed, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name

	pool, err := postgres.Open(ctx, parsed.String(), 8)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("удаление базы: %v", err)
		}
	})

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("миграции: %v", err)
	}

	return pool
}

// at — время с точностью базы.
func at(minutes int) time.Time {
	return time.Date(2026, 10, 23, 10, minutes, 0, 0, time.UTC)
}
