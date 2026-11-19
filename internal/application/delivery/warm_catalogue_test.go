package delivery_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type countingUOW struct {
	inner      ports.UnitOfWork
	tx         atomic.Int64
	public     atomic.Int64
	batches    atomic.Int64
	batchedKey atomic.Int64
}

type countingTx struct {
	ports.Tx
	u *countingUOW
}

type countingParticipants struct {
	participant.Repository
	u *countingUOW
}

type countingRegistry struct {
	participant.Registry
	u *countingUOW
}

func (u *countingUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	u.tx.Add(1)
	return u.inner.Do(
		ctx,
		func(ctx context.Context, tx ports.Tx) error {
			return fn(ctx, countingTx{Tx: tx, u: u})
		},
	)
}

func (t countingTx) Participants() participant.Repository {
	return countingParticipants{Repository: t.Tx.Participants(), u: t.u}
}

func (t countingTx) Registry() participant.Registry {
	return countingRegistry{Registry: t.Tx.Registry(), u: t.u}
}

func (r countingParticipants) PublicTenants(
	ctx context.Context,
	filter []ids.TenantID,
	after ids.TenantID,
	limit int,
) ([]ids.TenantID, error) {
	r.u.public.Add(1)
	return r.Repository.PublicTenants(ctx, filter, after, limit)
}

func (r countingParticipants) Batch(
	ctx context.Context,
	keys []ids.TenantID,
) (map[ids.TenantID]*participant.Participant, error) {
	r.u.batches.Add(1)
	r.u.batchedKey.Add(int64(len(keys)))
	return r.Repository.Batch(ctx, keys)
}

func (r countingRegistry) Batch(
	ctx context.Context,
	keys []ids.TenantID,
) (map[ids.TenantID]*participant.Tenant, error) {
	r.u.batches.Add(1)
	r.u.batchedKey.Add(int64(len(keys)))
	return r.Registry.Batch(ctx, keys)
}

// Тёплый кэш справочника: сколько работы с БД делает каждый запрос публичного списка ПВЗ
// (GET /api/v1/delivery/pickup-points, профиль покупателя) при 1000 магазинах.
func TestWarmPickupSearchDoesNotReadRegistry(t *testing.T) {
	store := fake.NewStore()
	platform := fake.NewPlatform()
	const shops = 1000
	for i := range shops {
		tenant := ids.TenantID(fmt.Sprintf("t%04d", i))
		store.SeedTenant(participant.TenantState{TenantID: tenant, Status: participant.TenantActive})
		store.SeedParticipant(participant.State{
			TenantID: tenant,
			Stage:    participant.StageConnected,
			Enabled:  true,
			Delivery: participant.DeliverySettings{Regions: []string{"RU-MOW"}, Carriers: []string{"cdek", "yandex"}},
		})
		platform.SetPickupPoints(
			tenant,
			ports.PickupPoints{
				Available: true,
				Items: []delivery.PickupPoint{{
					Carrier:    "cdek",
					Code:       "P1",
					Name:       "Пункт",
					City:       "Город",
					Address:    "Адрес",
					RegionCode: "RU-MOW",
					Location:   delivery.Location{Lat: 55, Lon: 37},
				}},
			},
		)
	}
	uow := &countingUOW{inner: store}
	service := appdelivery.NewService(
		uow,
		platform,
		fake.NewPickupCache(),
		appdelivery.Config{
			Attempts:     3,
			TTL:          time.Hour,
			QuoteTimeout: time.Second,
		},
	)
	ctx := context.Background()
	if _, err := service.Search(ctx, "cdek", "Город"); err != nil {
		t.Fatal(err)
	}
	cold := [4]int64{uow.tx.Load(), uow.public.Load(), uow.batches.Load(), uow.batchedKey.Load()}
	const warm = 100
	for range warm {
		if _, err := service.Search(ctx, "cdek", "Город"); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("холодный: tx=%d publicPages=%d batches=%d batchedKeys=%d", cold[0], cold[1], cold[2], cold[3])
	t.Logf(
		"%d тёплых запросов: tx=%d publicPages=%d batches=%d batchedKeys=%d; HTTP к серверу=%d",
		warm,
		uow.tx.Load()-cold[0],
		uow.public.Load()-cold[1],
		uow.batches.Load()-cold[2],
		uow.batchedKey.Load()-cold[3],
		len(platform.PickupCalls()),
	)
	if uow.tx.Load() > cold[0] {
		t.Fatalf("тёплый кэш справочника всё равно читает реестр всех магазинов на каждый запрос")
	}
}
