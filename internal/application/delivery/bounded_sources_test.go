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
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type sourceCounter struct {
	tx      atomic.Int64
	pages   atomic.Int64
	batches atomic.Int64
	rows    atomic.Int64
	sources atomic.Int64
}

type sourceUoW struct {
	inner ports.UnitOfWork
	c     *sourceCounter
}

type sourceTx struct {
	ports.Tx
	c *sourceCounter
}

type sourceParticipants struct {
	participant.Repository
	c *sourceCounter
}

type sourceRegistry struct {
	participant.Registry
	c *sourceCounter
}

func (u sourceUoW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	u.c.tx.Add(1)
	return u.inner.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return fn(ctx, sourceTx{Tx: tx, c: u.c})
	})
}

func (t sourceTx) Participants() participant.Repository {
	return sourceParticipants{Repository: t.Tx.Participants(), c: t.c}
}

func (t sourceTx) Registry() participant.Registry {
	return sourceRegistry{Registry: t.Tx.Registry(), c: t.c}
}

func (r sourceParticipants) PublicTenants(
	ctx context.Context,
	f []ids.TenantID,
	after ids.TenantID,
	n int,
) ([]ids.TenantID, error) {
	r.c.pages.Add(1)
	page, err := r.Repository.PublicTenants(ctx, f, after, n)
	r.c.rows.Add(int64(len(page)))
	return page, err
}

func (r sourceParticipants) Batch(
	ctx context.Context,
	keys []ids.TenantID,
) (map[ids.TenantID]*participant.Participant, error) {
	r.c.batches.Add(1)
	r.c.rows.Add(int64(len(keys)))
	return r.Repository.Batch(ctx, keys)
}

func (r sourceRegistry) Batch(ctx context.Context, keys []ids.TenantID) (map[ids.TenantID]*participant.Tenant, error) {
	r.c.batches.Add(1)
	r.c.rows.Add(int64(len(keys)))
	return r.Registry.Batch(ctx, keys)
}

// Новое имя города читает только ограниченную пачку подходящих источников.
func TestColdCatalogueReadsBoundedSources(t *testing.T) {
	for _, shops := range []int{1000, 5000} {
		store := fake.NewStore()
		platform := fake.NewPlatform()
		seedCatalogueSources(store, platform, shops)
		counter := &sourceCounter{}
		service := appdelivery.NewService(
			sourceUoW{inner: store, c: counter},
			platform,
			fake.NewPickupCache(),
			appdelivery.Config{Attempts: 3, TTL: time.Hour, QuoteTimeout: time.Second},
		)
		const cities = 10
		for i := range cities {
			_, err := service.Query(
				context.Background(),
				appdelivery.PickupQuery{
					Carrier: "cdek",
					City: fmt.Sprintf(
						"Новый %d",
						i,
					),
					Limit: 1,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
		}
		if counter.sources.Load() > cities*2 {
			t.Fatal("лишние индексные запросы", counter.sources.Load())
		}
		if counter.pages.Load() != 0 || counter.batches.Load() != 0 || counter.rows.Load() > cities*3 {
			t.Fatal(
				"холодный город прочитал реестр целиком",
				counter.pages.Load(),
				counter.batches.Load(),
				counter.rows.Load(),
			)
		}
		t.Logf(
			"магазинов %d: на %d новых городов — транзакций=%d, страниц реестра=%d, пакетных "+
				"чтений=%d, строк=%d (на город %d строк); вызовов магазинов=%d",
			shops,
			cities,
			counter.tx.Load(),
			counter.pages.Load(),
			counter.batches.Load(),
			counter.rows.Load(),
			counter.rows.Load()/cities,
			len(platform.PickupCalls()),
		)
	}
}

func (r sourceParticipants) PickupSources(
	ctx context.Context,
	carrier string,
	after ids.TenantID,
	limit int,
) ([]ids.TenantID, error) {
	r.c.sources.Add(1)
	rows, err := r.Repository.PickupSources(ctx, carrier, after, limit)
	r.c.rows.Add(int64(len(rows)))
	return rows, err
}
