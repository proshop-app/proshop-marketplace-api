//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const (
	seedLegacyParticipantPrice = `UPDATE participants SET subsidy = '{"enabled":true,"type":"percent","value":50}',
        free_delivery_from = 500000 WHERE tenant_id = 't1'`
	readLegacyParticipantPrice = `SELECT (subsidy->>'value')::bigint, free_delivery_from
        FROM participants WHERE tenant_id = 't1'`
)

func TestModernSettingsPreservePriceForOldProcesses(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	seedTenant(t, uow, "t1", "shop-one", participant.TenantActive)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Participants().Add(ctx, participant.Restore(participant.State{
			TenantID:  "t1",
			Slug:      "shop-one",
			Name:      "Магазин",
			Stage:     participant.StageConnected,
			Delivery:  w3RepositoriesIntegrationDelivery,
			Version:   1,
			CreatedAt: at(0),
			UpdatedAt: at(0),
		}))
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, seedLegacyParticipantPrice); err != nil {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, "t1")
		if err != nil {
			return err
		}
		if _, err := p.ApplyShopSettings(
			participant.DeliverySettings{
				Carriers: []string{"cdek"},
				Regions:  []string{"RU-MOW", "RU-MOS"},
			},
			at(1),
		); err != nil {
			return err
		}
		return tx.Participants().Update(ctx, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	var subsidy, threshold int64
	if err := pool.QueryRow(ctx, readLegacyParticipantPrice).Scan(&subsidy, &threshold); err != nil {
		t.Fatal(err)
	}
	if subsidy != 50 || threshold != 500000 {
		t.Fatal("новая синхронизация изменила цену старого процесса", subsidy, threshold)
	}
}
