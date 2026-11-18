package delivery_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

type delayedSourceUoW struct {
	inner  ports.UnitOfWork
	delay  time.Duration
	active *atomic.Int64
	peak   *atomic.Int64
}

func (u delayedSourceUoW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	active := u.active.Add(1)
	defer u.active.Add(-1)
	for old := u.peak.Load(); old < active && !u.peak.CompareAndSwap(old, active); old = u.peak.Load() {
	}
	select {
	case <-time.After(u.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	return u.inner.Do(ctx, fn)
}

// Медленные транзакции карты не сериализуют проверку выбранного ПВЗ покупки.
func TestSourceTransactionsDoNotSerializePointBehindMaps(t *testing.T) {
	store := fake.NewStore()
	platform := fake.NewPlatform()
	seedCatalogueSources(store, platform, 50)
	active, peak := &atomic.Int64{}, &atomic.Int64{}
	service := appdelivery.NewService(
		delayedSourceUoW{
			inner:  store,
			delay:  50 * time.Millisecond,
			active: active,
			peak:   peak,
		},
		platform,
		fake.NewPickupCache(),
		appdelivery.Config{Attempts: 3, TTL: time.Hour, QuoteTimeout: time.Second},
	)
	alone := time.Now()
	if _, err := service.Point(context.Background(), "buyer", "cdek", "P1", "Отдельный"); err != nil {
		t.Fatal(err)
	}
	single := time.Since(alone)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = service.Query(
				context.Background(),
				appdelivery.PickupQuery{
					Carrier: "cdek",
					City:    fmt.Sprintf("Карта %d", i),
					Limit:   1,
				},
			)
		}()
	}
	time.Sleep(time.Millisecond)
	start := time.Now()
	_, err := service.Point(context.Background(), "buyer", "cdek", "P1", "Холодный")
	elapsed := time.Since(start)
	wg.Wait()
	t.Logf(
		"БД 50 мс на транзакцию: Point один=%s; Point при 4 холодных загрузках карты=%s (err=%v)",
		single.Round(time.Millisecond),
		elapsed.Round(time.Millisecond),
		err,
	)
	if err != nil || elapsed > 3*single || peak.Load() < 2 {
		t.Fatal("Point ждал чужие транзакции выбора источников", elapsed, single, err, peak.Load())
	}
}
