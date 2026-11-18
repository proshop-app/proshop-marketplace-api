package delivery_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

func TestPickupCarrierRequiredBeforeCatalogueIO(t *testing.T) {
	store := fake.NewStore()
	uow := &countingUOW{inner: store}
	platform := fake.NewPlatform()
	service := appdelivery.NewService(uow, platform, fake.NewPickupCache(), appdelivery.Config{
		Attempts:     1,
		TTL:          time.Hour,
		QuoteTimeout: time.Second,
	})
	for _, carrier := range []delivery.Carrier{"", "all", "cdek,yandex", "ozon"} {
		_, err := service.Query(context.Background(), appdelivery.PickupQuery{
			Carrier: carrier,
			City:    "Город",
			Limit:   100,
		})
		if !errors.Is(err, delivery.ErrInvalidCarrier) {
			t.Fatalf("%q: %v", carrier, err)
		}
	}
	if uow.tx.Load() != 0 || len(platform.PickupCalls()) != 0 {
		t.Fatalf("недопустимая служба дошла до загрузки: БД=%d HTTP=%d", uow.tx.Load(), len(platform.PickupCalls()))
	}
}
