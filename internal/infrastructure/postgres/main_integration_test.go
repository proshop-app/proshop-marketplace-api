//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	return pgtest.Pool(t)
}

// at — время с точностью базы.
func at(minutes int) time.Time {
	return time.Date(2026, 10, 23, 10, minutes, 0, 0, time.UTC)
}
