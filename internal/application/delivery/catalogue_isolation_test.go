package delivery_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Тёплый справочник другой реплики читается до очереди холодных загрузок.
func TestRedisWarmCatalogAndPickupSurviveColdCityStorm(t *testing.T) {
	store := fake.NewStore()
	platform := fake.NewPlatform()
	seedCatalogueSources(store, platform, 50)
	shared := fake.NewPickupCache()
	config := appdelivery.Config{Attempts: 3, TTL: time.Hour, QuoteTimeout: 5 * time.Second}
	replica1 := appdelivery.NewService(store, platform, shared, config)
	if _, err := replica1.Query(
		context.Background(),
		appdelivery.PickupQuery{
			Carrier: "cdek",
			City:    "Город",
			Limit:   100,
		},
	); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	platform.BeforePickup = func(ctx context.Context, _ ids.TenantID) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	replica2 := appdelivery.NewService(store, platform, shared, config)
	for i := range 40 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		_, _ = replica2.Query(
			ctx,
			appdelivery.PickupQuery{
				Carrier: "cdek",
				City: fmt.Sprintf(
					"Атака %d",
					i,
				),
				Limit: 100,
			},
		)
		cancel()
	}
	_, warmErr := replica2.Query(
		context.Background(),
		appdelivery.PickupQuery{
			Carrier: "cdek",
			City:    "Город",
			Limit:   100,
		},
	)
	_, pointErr := replica2.Point(context.Background(), "cdek", "P1", "Город")
	t.Logf(
		"город в общем кэше, реплика 2 под штормом: карта=%v; Point=%v",
		warmErr,
		pointErr,
	)
	close(release)
	if warmErr != nil || pointErr != nil {
		t.Fatalf("тёплый в Redis город недоступен на реплике с заполненной очередью полётов")
	}
}

// Медленные холодные города не задерживают чтение Redis для тёплого города.
func TestRedisWarmCatalogDoesNotWaitForColdLoads(t *testing.T) {
	store := fake.NewStore()
	platform := fake.NewPlatform()
	seedCatalogueSources(store, platform, 50)
	shared := fake.NewPickupCache()
	config := appdelivery.Config{Attempts: 3, TTL: time.Hour, QuoteTimeout: 5 * time.Second}
	replica1 := appdelivery.NewService(store, platform, shared, config)
	if _, err := replica1.Query(
		context.Background(),
		appdelivery.PickupQuery{
			Carrier: "cdek",
			City:    "Город",
			Limit:   100,
		},
	); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	platform.BeforePickup = func(ctx context.Context, _ ids.TenantID) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	replica2 := appdelivery.NewService(store, platform, shared, config)
	for i := range 4 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		_, _ = replica2.Query(
			ctx,
			appdelivery.PickupQuery{
				Carrier: "cdek",
				City: fmt.Sprintf(
					"Медленный %d",
					i,
				),
				Limit: 100,
			},
		)
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	start := time.Now()
	_, warmErr := replica2.Query(ctx, appdelivery.PickupQuery{Carrier: "cdek", City: "Город", Limit: 100})
	cancel()
	t.Logf(
		"4 медленных холодных города; тёплый в Redis город: %v за %s",
		warmErr,
		time.Since(start).Round(time.Millisecond),
	)
	close(release)
	if warmErr != nil {
		t.Fatalf("чтение Redis тёплого города стоит в очереди за медленными загрузками")
	}
}

func TestColdPickupSelectionHasReservedCapacityDuringAnonymousStorm(t *testing.T) {
	store, platform := fake.NewStore(), fake.NewPlatform()
	seedCatalogueSources(store, platform, 50)
	release := make(chan struct{})
	defer close(release)
	var started atomic.Int32
	platform.BeforePickup = func(ctx context.Context, _ ids.TenantID) error {
		if ports.PlatformClassFromContext(ctx) == ports.PlatformCheckout {
			return nil
		}
		started.Add(1)
		select {
		case <-release:
			return nil
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
	for i := range 40 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		_, _ = service.Query(
			ctx,
			appdelivery.PickupQuery{
				Carrier: "cdek",
				City: fmt.Sprintf(
					"Атака %d",
					i,
				),
				Limit: 100,
			},
		)
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	point, err := service.Point(ctx, "cdek", "P1", "Город")
	if err != nil || point.Code != "P1" || started.Load() > 4 {
		t.Fatal(point, err, started.Load())
	}
	// Результат оформления наполняет общий кэш: карта читает его даже при полной анонимной очереди.
	area, err := service.Query(ctx, appdelivery.PickupQuery{Carrier: "cdek", City: "Город", Limit: 100})
	if err != nil || len(area.Items) != 1 {
		t.Fatal(area, err)
	}
}
