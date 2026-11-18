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
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Ф3, остаточный риск без справочника городов: сколько загрузок в магазины вызывает один анонимный
// клиент новыми именами городов. Ответ источника — «пунктов нет» за L; клиент держит 64 запроса.
func TestAnonymousCityLoadRateStaysWithinMapConcurrency(t *testing.T) {
	for _, latency := range []time.Duration{50 * time.Millisecond, 200 * time.Millisecond} {
		store := fake.NewStore()
		platform := fake.NewPlatform()
		seedCatalogueSources(store, platform, 1000)
		for i := range 1000 {
			platform.SetPickupPoints(
				ids.TenantID(fmt.Sprintf("t%04d", i)),
				ports.PickupPoints{Available: true, Items: []delivery.PickupPoint{}},
			)
		}
		stopLoads := make(chan struct{})
		var active, peak atomic.Int64
		var upstream atomic.Int64
		var perTenant sync.Map
		platform.BeforePickup = func(ctx context.Context, tenant ids.TenantID) error {
			select {
			case <-stopLoads:
				return context.Canceled
			default:
			}
			now := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); old < now && !peak.CompareAndSwap(old, now); old = peak.Load() {
			}
			upstream.Add(1)
			counter, _ := perTenant.LoadOrStore(tenant, new(atomic.Int64))
			counter.(*atomic.Int64).Add(1)
			select {
			case <-time.After(latency):
				return nil
			case <-stopLoads:
				return context.Canceled
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		service := appdelivery.NewService(
			store,
			platform,
			fake.NewPickupCache(),
			appdelivery.Config{
				Attempts:     3,
				TTL:          time.Hour,
				QuoteTimeout: 5 * time.Second,
			},
		)
		window := 3 * time.Second
		deadline := time.Now().Add(window)
		var next atomic.Int64
		var refused atomic.Int64
		var wg sync.WaitGroup
		for range 64 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for time.Now().Before(deadline) {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					_, err := service.Query(
						ctx,
						appdelivery.PickupQuery{
							Carrier: "cdek",
							City:    fmt.Sprintf("Имя %d", next.Add(1)),
							Limit:   1,
						},
					)
					cancel()
					if err != nil {
						refused.Add(1)
						time.Sleep(time.Millisecond)
					}
				}
			}()
		}
		time.Sleep(time.Until(deadline))
		observed := upstream.Load()
		close(stopLoads)
		wg.Wait()
		if peak.Load() > 4 {
			t.Fatal("карта превысила квоту", peak.Load())
		}
		tenants, maxPerTenant := 0, int64(0)
		perTenant.Range(
			func(_, value any) bool {
				tenants++
				if n := value.(*atomic.Int64).Load(); n > maxPerTenant {
					maxPerTenant = n
				}
				return true
			},
		)
		perMinute := float64(observed) * float64(time.Minute) / float64(window)
		t.Logf(
			"L=%s: запросов клиента=%d, отказов=%d, вызовов магазинов=%d за %s → %.0f/мин на "+
				"реплику; магазинов задето=%d, максимум на магазин=%d",
			latency,
			next.Load(),
			refused.Load(),
			observed,
			window,
			perMinute,
			tenants,
			maxPerTenant,
		)
	}
}
