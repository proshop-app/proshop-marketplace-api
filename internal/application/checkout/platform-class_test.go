package checkout_test

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestPurchaseClassesAndCallBudget(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.carts.Remove(ctx, buyerID, p2); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := map[string]int{}
	e.platform.CallHook = func(ctx context.Context, operation string) error {
		if operation == "price_group" || operation == "settings" {
			if ports.PlatformClassFromContext(ctx) != ports.PlatformCheckout {
				t.Error("покупательский вызов попал в чтения", operation)
			}
		}
		mu.Lock()
		calls[operation]++
		mu.Unlock()
		return nil
	}
	e.platform.BeforePickup = func(ctx context.Context, _ ids.TenantID) error {
		if ports.PlatformClassFromContext(ctx) != ports.PlatformCheckout {
			t.Error("выбор ПВЗ попал в чтения")
		}
		mu.Lock()
		calls["pickup_points"]++
		mu.Unlock()
		return nil
	}
	// Одна правка корзины, один магазин, холодный ПВЗ, свежий quote, без повторов и сверок.
	if _, err := e.carts.SetQuantity(ctx, buyerID, p1, 2); err != nil {
		t.Fatal(err)
	}
	c := confirmedPurchase(t, e)
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"price_group":   3,
		"pickup_points": 1,
		"quote":         2,
		"create_order":  1,
		"stub_payment":  1,
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatal("изменился расчёт обращений на покупку", calls)
	}
}
