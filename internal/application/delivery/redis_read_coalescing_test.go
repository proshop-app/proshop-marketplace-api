package delivery_test

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type sharedReadCounter struct {
	inner *fake.PickupCache
	gets  atomic.Int64
	bytes atomic.Int64
}

func (c *sharedReadCounter) Get(ctx context.Context, key string) ([]byte, bool, error) {
	value, hit, err := c.inner.Get(ctx, key)
	c.gets.Add(1)
	c.bytes.Add(int64(len(value)))
	return value, hit, err
}

func (c *sharedReadCounter) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return c.inner.Set(ctx, key, value, ttl)
}

// После перезапуска реплики большой снимок Redis читается и разбирается один раз на ключ.
func TestRedisCatalogueReadCoalescesConcurrentRequests(t *testing.T) {
	store := fake.NewStore()
	platform := fake.NewPlatform()
	seedCatalogueSources(store, platform, 4)
	points := make([]delivery.PickupPoint, 0, 20000)
	for i := range 20000 {
		points = append(
			points,
			delivery.PickupPoint{
				Carrier:    "cdek",
				Code:       fmt.Sprintf("P%05d", i),
				Name:       "Пункт выдачи",
				City:       "Москва",
				Address:    fmt.Sprintf("ул. Примерная, %d", i),
				RegionCode: "RU-MOW",
				Location:   delivery.Location{Lat: 55 + float64(i%1000)/1000, Lon: 37 + float64(i/1000)/100},
			},
		)
	}
	for i := range 4 {
		platform.SetPickupPoints(
			ids.TenantID(fmt.Sprintf("t%04d", i)),
			ports.PickupPoints{Available: true, Items: points},
		)
	}
	shared := &sharedReadCounter{inner: fake.NewPickupCache()}
	config := appdelivery.Config{Attempts: 3, TTL: time.Hour, QuoteTimeout: 5 * time.Second}
	replica1 := appdelivery.NewService(store, platform, shared, config)
	if _, err := replica1.Query(
		context.Background(),
		appdelivery.PickupQuery{
			Carrier: "cdek",
			City:    "Москва",
			Limit:   100,
		},
	); err != nil {
		t.Fatal(err)
	}
	for _, concurrent := range []int{1, 200} {
		replica := appdelivery.NewService(store, platform, shared, config)
		shared.gets.Store(0)
		shared.bytes.Store(0)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		var wg sync.WaitGroup
		for range concurrent {
			wg.Add(1)
			go func() {
				defer wg.Done()
				area, err := replica.Query(context.Background(), appdelivery.PickupQuery{
					Carrier: "cdek",
					City:    "Москва",
					Limit:   100,
				})
				if err != nil || len(area.Items) != 100 {
					t.Error("справочник должен вернуть страницу", len(area.Items), err)
				}
			}()
		}
		wg.Wait()
		if shared.gets.Load() != 1 {
			t.Fatalf("Redis должен читаться один раз: %d", shared.gets.Load())
		}
		runtime.ReadMemStats(&after)
		t.Logf(
			"холодная реплика, %d одновременных запросов тёплого в Redis города (20 000 ПВЗ):"+
				" чтений Redis=%d, байт из Redis=%d МБ, выделено=%d МБ, за %s",
			concurrent,
			shared.gets.Load(),
			shared.bytes.Load()>>20,
			(after.TotalAlloc-before.TotalAlloc)>>20,
			time.Since(start).Round(time.Millisecond),
		)
	}
}
