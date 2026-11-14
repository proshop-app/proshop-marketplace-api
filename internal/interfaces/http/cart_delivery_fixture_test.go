package http_test

import (
	"context"
	"testing"
	"time"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
)

// Проверяет тот же набор данных, что и сквозной HTTP-тест, без открытия сокета.
func TestCartDeliveryPickupFixture(t *testing.T) {
	e := &env{
		store:    fake.NewStore(),
		index:    fake.NewIndex(),
		platform: fake.NewPlatform(),
		pickups:  &fake.PickupLookup{},
	}
	e.seedCartDelivery()
	service := appdelivery.NewService(e.store, e.platform, fake.NewPickupCache(), appdelivery.Config{
		Attempts:     3,
		TTL:          time.Hour,
		QuoteTimeout: time.Second,
	})
	points, err := service.Search(context.Background(), "cdek", "Город")
	if err != nil || len(points) != 1 || points[0].Code != "MSK1" {
		t.Fatal(points, err)
	}
	calls := e.platform.PickupCalls()
	if len(calls) != 1 || calls[0].Tenant != "t1" || calls[0].City != "Город" {
		t.Fatal(calls)
	}
}
