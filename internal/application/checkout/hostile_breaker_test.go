package checkout_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/platformguard"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// 409 MARKETPLACE_PICKUP_CATALOG_CHANGED приходит в площадку как неожиданный отказ (platformapi.refusal).
// Пять сбоев загрузки справочника открывают размыкатель магазина, общий для quote и CreateOrder.
func TestHostileDirectoryFailuresOpenShopBreakerForQuotes(t *testing.T) {
	e := setup(t)
	e.platform.BeforePickup = func(context.Context, ids.TenantID) error {
		return &ports.OrderRefusal{Code: "MARKETPLACE_PICKUP_CATALOG_CHANGED", Unexpected: true}
	}
	guard := platformguard.New(
		e.platform,
		platformguard.Config{
			StrictTimeout:         time.Second,
			MaxConcurrent:         16,
			CheckoutMaxConcurrent: 8,
			ReadMaxConcurrent:     8,
			TenantMaxConcurrent:   8,
			QueueSize:             32,
			TenantQueueSize:       8,
			TenantStateSize:       100,
			TenantIdleTTL:         time.Minute,
			FailureThreshold:      5,
			FailureWindow:         30 * time.Second,
			FailureMinCalls:       20,
			FailurePercent:        50,
			OpenInterval:          5 * time.Second,
		},
		e.clock,
		nil,
		nil,
	)
	ctx := ports.WithPlatformCallClass(context.Background(), ports.PlatformCheckout)
	for range 5 {
		_, err := guard.PickupPoints(ctx, "t2", "cdek", "Город")
		t.Logf("справочник: %v", err)
	}
	_, err := guard.QuoteDelivery(
		ctx,
		"t2",
		ports.QuoteRequest{
			Carrier:         "cdek",
			PickupPointCode: "MSK1",
			City:            "Город",
			Items:           []ports.ItemRequest{{ProductID: "p1", Quantity: 1}},
		},
	)
	t.Logf("quote после сбоев справочника: %v", err)
	if errors.Is(err, platformguard.ErrUnavailable) {
		q := delivery.Unreachable(e.clock.Now())
		t.Fatalf("расчёт магазина отклонён размыкателем из-за справочника; checkout получит %s/%s", q.Status, q.Reason)
	}
}
