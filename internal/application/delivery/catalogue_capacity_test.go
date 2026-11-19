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
	"github.com/proshop-app/proshop-marketplace/internal/application/platformguard"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func seedCatalogueSources(store *fake.Store, platform *fake.Platform, count int) {
	for i := range count {
		tenant := ids.TenantID(fmt.Sprintf("t%04d", i))
		store.SeedTenant(participant.TenantState{TenantID: tenant, Status: participant.TenantActive})
		store.SeedParticipant(
			participant.State{
				TenantID: tenant,
				Stage:    participant.StageConnected,
				Enabled:  true,
				Delivery: participant.DeliverySettings{
					Regions:  []string{"RU-MOW"},
					Carriers: []string{"cdek", "yandex"},
				},
			},
		)
		platform.SetPickupPoints(
			tenant,
			ports.PickupPoints{
				Available: true,
				Items: []delivery.PickupPoint{
					{
						Carrier:    "cdek",
						Code:       "P1",
						Name:       "Пункт",
						City:       "Город",
						Address:    "Адрес",
						RegionCode: "RU-MOW",
						Location:   delivery.Location{Lat: 55, Lon: 37},
					},
				},
			},
		)
	}
}

// Публичная ручка без входа: 50 запросов разных городов, клиент сразу уходит.
// Сколько фоновых загрузок остаётся и что они делают с расчётом самого первого магазина.
func TestColdCatalogueStormLeavesCheckoutCapacity(t *testing.T) {
	store := fake.NewStore()
	platform := fake.NewPlatform()
	seedCatalogueSources(store, platform, 1000)
	var active, peak atomic.Int64
	release := make(chan struct{})
	var tenants sync.Map
	platform.BeforePickup = func(ctx context.Context, tenant ids.TenantID) error {
		tenants.Store(tenant, true)
		now := active.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		defer active.Add(-1)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	guard := platformguard.New(
		platform,
		platformguard.Config{
			StrictTimeout:         5 * time.Second,
			MaxConcurrent:         128,
			CheckoutMaxConcurrent: 64,
			ReadMaxConcurrent:     64,
			TenantMaxConcurrent:   8,
			QueueSize:             1024,
			TenantQueueSize:       32,
			TenantStateSize:       10000,
			TenantIdleTTL:         10 * time.Minute,
			FailureThreshold:      5,
			FailureWindow:         30 * time.Second,
			FailureMinCalls:       20,
			FailurePercent:        50,
			OpenInterval:          5 * time.Second,
		},
		fake.NewClock(time.Now()),
		nil,
		nil,
	)
	service := appdelivery.NewService(
		store,
		guard,
		fake.NewPickupCache(),
		appdelivery.Config{
			Attempts:     3,
			TTL:          time.Hour,
			QuoteTimeout: 5 * time.Second,
		},
	)
	for i := range 50 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		query := appdelivery.PickupQuery{Carrier: "cdek", City: fmt.Sprintf("Город %d", i), Limit: 100}
		_, _ = service.Query(ctx, query)
		cancel()
	}
	time.Sleep(200 * time.Millisecond)
	quoteCtx, stop := context.WithTimeout(
		ports.WithPlatformCallClass(context.Background(), ports.PlatformCheckout),
		300*time.Millisecond,
	)
	_, quoteErr := guard.QuoteDelivery(
		quoteCtx,
		"t0000",
		ports.QuoteRequest{
			Carrier:         "cdek",
			PickupPointCode: "P1",
			City:            "Город",
			Items:           []ports.ItemRequest{{ProductID: "p1", Quantity: 1}},
		},
	)
	stop()
	shops := 0
	tenants.Range(func(any, any) bool { shops++; return true })
	message := "одновременно: %d (пик %d), источников: %d; quote магазина t0000: %v"
	t.Logf(
		message,
		active.Load(),
		peak.Load(),
		shops,
		quoteErr,
	)
	close(release)
	if peak.Load() > 4 {
		t.Fatal("превышен предел загрузок", peak.Load())
	}
	if quoteErr != nil {
		t.Fatalf("фоновые загрузки справочника заняли места первого магазина: его расчёт для покупки отклонён")
	}
}

// Пустой город разделяет короткий кэш без повторного обхода реестра и HTTP.
func TestEmptyCitySharesNegativeCache(t *testing.T) {
	store := fake.NewStore()
	platform := fake.NewPlatform()
	seedCatalogueSources(store, platform, 1000)
	for i := range 1000 {
		platform.SetPickupPoints(ids.TenantID(fmt.Sprintf("t%04d", i)), ports.PickupPoints{Available: true})
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
	for range 100 {
		query := appdelivery.PickupQuery{Carrier: "cdek", City: "Мосвка", Limit: 100}
		if _, err := service.Query(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf(
		"100 запросов пустого города: tx=%d publicPages=%d batchedKeys=%d HTTP к серверу=%d",
		uow.tx.Load(),
		uow.public.Load(),
		uow.batchedKey.Load(),
		len(platform.PickupCalls()),
	)
	if len(platform.PickupCalls()) > 1 {
		t.Fatalf("пустой ответ не кэшируется ни в памяти, ни в Redis площадки")
	}
}

// Оба покупателя получают один справочник через резервный исправный источник.
func TestSharedCatalogueFallsBackAcrossShopSources(t *testing.T) {
	store := fake.NewStore()
	platform := fake.NewPlatform()
	seedCatalogueSources(store, platform, 2)
	platform.Fail("t0000", context.DeadlineExceeded)
	entered := make(chan struct{})
	platform.BeforePickup = func(ctx context.Context, tenant ids.TenantID) error {
		if tenant == "t0000" {
			close(entered)
			time.Sleep(100 * time.Millisecond)
		}
		return nil
	}
	service := appdelivery.NewService(
		store,
		platform,
		fake.NewPickupCache(),
		appdelivery.Config{
			Attempts:     3,
			TTL:          time.Hour,
			QuoteTimeout: time.Second,
		},
	)
	box := delivery.Bounds{54, 36, 56, 38}
	first := make(chan error, 1)
	go func() {
		_, err := service.QueryFor(
			context.Background(),
			"cdek",
			"Город",
			[]string{"RU-MOW"},
			&box,
			0,
			500,
		)
		first <- err
	}()
	<-entered
	_, secondErr := service.QueryFor(
		context.Background(),
		"cdek",
		"Город",
		[]string{"RU-MOW"},
		&box,
		0,
		500,
	)
	t.Logf("покупка с A: %v; одновременная покупка с исправным B: %v", <-first, secondErr)
	if secondErr != nil {
		t.Fatalf("исправный магазин B получил отказ из-за чужой загрузки через A")
	}
}
