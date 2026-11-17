//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const reportWriteObjects = `SELECT
 to_regclass('read_report_changes') IS NOT NULL OR to_regclass('read_reports') IS NOT NULL,
 EXISTS (SELECT 1 FROM pg_trigger WHERE NOT tgisinternal AND tgname LIKE 'read_report_%')`

func TestModelKeyReportHasNoWriteTrackingAndRunsReadOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var tables, triggers bool
	if err := pool.QueryRow(ctx, reportWriteObjects).Scan(&tables, &triggers); err != nil {
		t.Fatal(err)
	}
	if tables || triggers {
		t.Fatal("диагностический отчёт добавил учёт изменений на пути событий")
	}
	config := pool.Config()
	config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	reader, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := postgres.NewModelKeyStats(reader).Report(ctx, false); err != nil {
		t.Fatal("отчёту понадобилась запись в базу", err)
	}
}
