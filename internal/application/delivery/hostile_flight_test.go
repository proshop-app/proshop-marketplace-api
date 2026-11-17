package delivery_test

import (
	"context"
	"testing"
	"time"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Витрина отменяет запрос карты при каждом движении; общая холодная загрузка умирает вместе с последним ожидающим.
func TestHostileAbortedMapRequestRestartsColdLoad(t *testing.T) {
	store := fake.NewStore()
	platform := fake.NewPlatform()
	store.SeedTenant(participant.TenantState{TenantID: "t1", Status: participant.TenantActive})
	store.SeedParticipant(participant.State{
		TenantID: "t1",
		Stage:    participant.StageConnected,
		Enabled:  true,
		Delivery: participant.DeliverySettings{Regions: []string{"RU-MOW"}, Carriers: []string{"cdek"}},
	})
	platform.SetPickupPoints(
		"t1",
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
	// Холодный большой город: сервер отдаёт страницы 2 секунды.
	platform.BeforePickup = func(ctx context.Context, _ ids.TenantID) error {
		started := time.Now()
		select {
		case <-ctx.Done():
			println("загрузка отменена через", time.Since(started).String())
			return ctx.Err()
		case <-time.After(2 * time.Second):
			println("загрузка завершена через", time.Since(started).String())
			return nil
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
	box := delivery.Bounds{54, 36, 56, 38}
	for move := range 5 {
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		_, err := service.QueryFor(ctx, "cdek", "Город", []ids.TenantID{"t1"}, []string{"RU-MOW"}, &box, 0, 500)
		cancel()
		time.Sleep(20 * time.Millisecond)
		t.Logf("движение %d: err=%v, загрузок у сервера=%d", move, err, len(platform.PickupCalls()))
	}
	if len(platform.PickupCalls()) > 1 {
		t.Fatalf("каждый отменённый запрос карты заново начинает холодную загрузку: %d", len(platform.PickupCalls()))
	}
}
