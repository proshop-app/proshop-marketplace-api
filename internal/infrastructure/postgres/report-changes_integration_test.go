//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const readReportVersion = `SELECT COALESCE(sum(version),0)::bigint FROM read_report_changes`

func TestReportChangesIgnoreSettingsHeartbeat(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	seedReadParticipant(t, uow, "t1", false)
	version := func() int64 {
		t.Helper()
		var result int64
		if err := pool.QueryRow(ctx, readReportVersion).Scan(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := version()
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, "t1")
		if err != nil {
			return err
		}
		if _, err := p.ApplyShopSettings(
			w3RepositoriesIntegrationSubsidy,
			money.Money{},
			w3RepositoriesIntegrationDelivery,
			at(3),
		); err != nil {
			return err
		}
		return tx.Participants().SettingsChecked(ctx, p)
	})
	if err != nil || version() != before {
		t.Fatalf("heartbeat изменил отчёт: %v", err)
	}
	stats := postgres.NewModelKeyStats(pool)
	for i := 0; i < 2; i++ {
		if _, err := stats.Report(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
}
