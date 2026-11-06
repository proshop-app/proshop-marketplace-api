//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const deleteSellerParticipant = `DELETE FROM participants WHERE tenant_id = $1`

func TestParticipantSellersBatch(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	for _, id := range []ids.TenantID{"t1", "t2"} {
		seedTenant(t, uow, id, "shop-"+string(id), participant.TenantActive)
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			tenant, err := tx.Registry().Get(ctx, id)
			if err != nil {
				return err
			}
			p, err := participant.New(tenant, "Магазин "+string(id), at(0))
			if err != nil {
				return err
			}
			return tx.Participants().Add(ctx, p)
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, deleted := range []bool{false, true} {
		if deleted {
			if _, err := pool.Exec(ctx, deleteSellerParticipant, "t2"); err != nil {
				t.Fatal(err)
			}
		}
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			sellers, err := tx.Participants().Sellers(ctx, []ids.TenantID{"t1", "t2", "t1", "missing"})
			if err != nil {
				return err
			}
			expected := 2
			if deleted {
				expected = 1
			}
			if len(sellers) != expected || sellers["missing"] != nil || sellers["t1"] == nil {
				t.Fatalf("продавцы: %+v", sellers)
			}
			if *sellers["t1"] != (participant.Seller{Slug: "shop-t1", Name: "Магазин t1"}) {
				t.Fatal(sellers["t1"])
			}
			empty, err := tx.Participants().Sellers(ctx, nil)
			if err == nil && len(empty) != 0 {
				t.Fatal(empty)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}
