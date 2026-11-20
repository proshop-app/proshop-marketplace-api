//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func seedReadParticipant(t *testing.T, uow *postgres.UnitOfWork, tenant ids.TenantID, enabled bool) {
	t.Helper()
	seedTenant(t, uow, tenant, string(tenant), participant.TenantActive)
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		record, err := tx.Registry().Get(ctx, tenant)
		if err != nil {
			return err
		}
		p, err := participant.New(record, "Магазин", at(0))
		if err != nil {
			return err
		}
		if _, err := p.ApplyShopSettings(
			w3RepositoriesIntegrationDelivery,
			at(1),
		); err != nil {
			return err
		}
		if enabled {
			if _, err := p.ApplyStoreCard(
				participant.Profile{},
				participant.CardReadiness{Ready: true},
				nil,
				at(1),
			); err != nil {
				return err
			}
			if _, err := p.Enable(at(1)); err != nil {
				return err
			}
		}
		return tx.Participants().Add(ctx, p)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestParticipantBatches(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	seedReadParticipant(t, uow, "t1", true)
	seedReadParticipant(t, uow, "t2", false)
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		keys := []ids.TenantID{"t1", "t2", "missing"}
		participants, err := tx.Participants().Batch(ctx, keys)
		if err != nil || len(participants) != 2 {
			t.Fatalf("participants=%v err=%v", participants, err)
		}
		tenants, err := tx.Registry().Batch(ctx, keys)
		if err != nil || len(tenants) != 2 {
			t.Fatalf("tenants=%v err=%v", tenants, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
