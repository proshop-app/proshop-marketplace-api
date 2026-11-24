package delivery_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	app "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestCatalogueFailureAndDisconnectedSourceNeverBecomeEmptySuccess(t *testing.T) {
	_, platform, store := setup(t, 3)
	cache := fake.NewPickupCache()
	service := app.NewService(store, guarded(platform, 20*time.Millisecond), cache, app.Config{
		Attempts:     3,
		TTL:          time.Hour,
		QuoteTimeout: 20 * time.Millisecond,
	})
	platform.BeforePickup = func(ctx context.Context, tenant ids.TenantID) error {
		if tenant == "t1" {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil // t2: carrier_not_connected, а не успешный пустой справочник.
	}
	for range 2 {
		if _, err := service.Search(context.Background(), "cdek", "Город"); !errors.Is(err, app.ErrSellerUnreachable) {
			t.Fatal("отказ загрузки превратился в пустой успех", err)
		}
	}
	if len(platform.PickupCalls()) != 2 {
		t.Fatal("короткий кэш отказа не сработал", platform.PickupCalls())
	}
	raw, ok, err := cache.Get(context.Background(), "v5:cdek:город")
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	var cached struct {
		Failed    bool      `json:"failed"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal(raw, &cached); err != nil {
		t.Fatal(err)
	}
	if !cached.Failed || time.Until(cached.ExpiresAt) > 5*time.Second {
		t.Fatal("отказ должен сохраняться на 5 с, а не как пустой город на 30 с", cached)
	}
	// Следующий источник отвечает: свежая реплика после короткого отказа загружает реальные пункты.
	platform.BeforePickup = nil
	platform.SetPickupPoints("t2", answer())
	service = app.NewService(store, guarded(platform, 20*time.Millisecond), fake.NewPickupCache(), app.Config{
		Attempts:     3,
		TTL:          time.Hour,
		QuoteTimeout: 20 * time.Millisecond,
	})
	points, err := service.Search(context.Background(), "cdek", "Город")
	if err != nil || len(points) != 1 {
		t.Fatal(points, err)
	}
}

func TestEmptyCatalogueIsSuccessfulOnlyAfterAnAvailableSource(t *testing.T) {
	service, platform, _ := setup(t, 3)
	platform.SetPickupPoints("t1", ports.PickupPoints{Available: true})
	for range 2 {
		points, err := service.Search(context.Background(), "cdek", "Город")
		if err != nil || len(points) != 0 {
			t.Fatal(points, err)
		}
	}
	if len(platform.PickupCalls()) != 1 {
		t.Fatal(platform.PickupCalls())
	}
}
