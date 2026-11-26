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

const connectSellerParticipant = `UPDATE participants SET stage = 'connected', enabled = true, delivery_valid = true,
    delivery_settings = '{"regions": ["RU-MOW"], "carriers": ["cdek", "yandex"]}' WHERE tenant_id = $1`

func TestParticipantSellersBatch(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	seedSellerParticipants(t, uow, "t1", "t2")
	if _, err := pool.Exec(ctx, connectSellerParticipant, "t2"); err != nil {
		t.Fatal(err)
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
			if len(sellers.Items) != expected || sellers.Items["missing"] != nil || sellers.Items["t1"] == nil {
				t.Fatalf("продавцы: %+v", sellers)
			}
			want := participant.Seller{Slug: "shop-t1", Name: "Магазин t1", UnavailableReason: "delivery_unavailable"}
			if *sellers.Items["t1"] != want {
				t.Fatal(sellers.Items["t1"])
			}
			// Подключённый магазин с настроенной доставкой в корзине доступен: стадия читается запросом.
			if !deleted && sellers.Items["t2"].UnavailableReason != "" {
				t.Fatal(sellers.Items["t2"])
			}
			empty, err := tx.Participants().Sellers(ctx, nil)
			if err == nil && len(empty.Items) != 0 {
				t.Fatal(empty)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func seedSellerParticipants(t *testing.T, uow *postgres.UnitOfWork, tenants ...ids.TenantID) {
	t.Helper()
	ctx := context.Background()
	for _, id := range tenants {
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
}

func TestParticipantSellersIsolatesCorruptRow(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	seedSellerParticipants(t, uow, "t1", "t2")
	for _, settings := range []string{`{"regions":{}}`, "null"} {
		if _, err := pool.Exec(ctx, setSellerDelivery, "t1", settings); err != nil {
			t.Fatal(err)
		}
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			sellers, err := tx.Participants().Sellers(ctx, []ids.TenantID{"t1", "t2", "missing"})
			if err != nil {
				return err
			}
			if len(sellers.Failures) != 1 || sellers.Failures["t1"] == nil ||
				len(sellers.Items) != 1 || sellers.Items["t2"] == nil || sellers.Items["t1"] != nil {
				t.Fatalf("настройки %s: продавцы и ошибки по магазинам: %+v", settings, sellers)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
