package delivery_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
)

// Замер тёплого ключа при 100 и 20 000 городах: очистка не сканирует весь кэш на запрос.
func TestWarmCatalogueCostWithManyCachedCities(t *testing.T) {
	for _, n := range []int{100, 20000} {
		store := fake.NewStore()
		platform := fake.NewPlatform()
		seedCatalogueSources(store, platform, 4)
		uow := &countingUOW{inner: store}
		service := appdelivery.NewService(
			uow,
			platform,
			fake.NewPickupCache(),
			appdelivery.Config{
				Attempts:     1,
				TTL:          time.Hour,
				QuoteTimeout: time.Second,
			},
		)
		var wg sync.WaitGroup
		sem := make(chan struct{}, 30)
		for i := range n {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				_, _ = service.Query(
					context.Background(),
					appdelivery.PickupQuery{
						Carrier: "cdek",
						City: fmt.Sprintf(
							"Пусто %d",
							i,
						),
						Limit: 1,
					},
				)
			}()
		}
		wg.Wait()
		_, _ = service.Query(
			context.Background(),
			appdelivery.PickupQuery{
				Carrier: "cdek",
				City:    "Пусто 0",
				Limit:   1,
			},
		)
		uow.tx.Store(0)
		calls := len(platform.PickupCalls())
		start := time.Now()
		const warm = 2000
		for range warm {
			_, _ = service.Query(
				context.Background(),
				appdelivery.PickupQuery{
					Carrier: "cdek",
					City:    "Пусто 0",
					Limit:   1,
				},
			)
		}
		if uow.tx.Load() != 0 || len(platform.PickupCalls()) != calls {
			t.Fatal("тёплый ключ повторно загружает справочник")
		}
		t.Logf(
			"городов в памяти: %d; тёплый запрос: %s в среднем (под общим mu)",
			n,
			time.Since(start)/warm,
		)
	}
}
