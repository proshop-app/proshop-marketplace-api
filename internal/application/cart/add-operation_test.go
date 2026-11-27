package cart_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
)

func TestCartAddKeyAcrossReplicas(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	second := appcart.NewService(e.store, e.platform, &fake.IDs{}, e.clock, time.Hour, "test-cart-key")
	key := e.service.NewAddKey(buyerID)
	attempts := 0
	e.platform.CallHook = func(ctx context.Context, operation string) error {
		attempts++
		if attempts == 1 {
			if _, err := second.AddOnce(ctx, buyerID, p1, 1, key); !errors.Is(err, cart.ErrAddInProgress) {
				t.Error(err)
			}
		}
		return nil
	}
	first, err := e.service.AddOnce(ctx, buyerID, p1, 1, key)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := second.AddOnce(ctx, buyerID, p1, 1, key)
	if err != nil || replay.Version != first.Version || replay.Lines[0].Quantity != 1 || attempts != 1 {
		t.Fatal(replay, attempts, err)
	}
	if _, err := second.AddOnce(ctx, buyerID, p2, 1, key); !errors.Is(err, cart.ErrAddKeyReused) {
		t.Fatal(err)
	}
	if _, err := second.AddOnce(ctx, "other-buyer", p1, 1, key); !errors.Is(err, cart.ErrAddKey) {
		t.Fatal(err)
	}
	e.clock.Advance(24 * time.Hour)
	if _, err := second.AddOnce(ctx, buyerID, p1, 1, key); !errors.Is(err, cart.ErrAddKey) || attempts != 1 {
		t.Fatal(attempts, err)
	}
}

func TestCartAddKeyCanRecoverAfterPricingFailure(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	key := e.service.NewAddKey(buyerID)
	e.platform.CallHook = func(context.Context, string) error { return errors.New("нет связи") }
	if _, err := e.service.AddOnce(ctx, buyerID, p1, 1, key); !errors.Is(err, appcart.ErrSellerUnreachable) {
		t.Fatal(err)
	}
	if _, ok := e.store.Cart(buyerID); ok {
		t.Fatal("неудачное добавление изменило корзину")
	}
	e.platform.CallHook = nil
	state, err := e.service.AddOnce(ctx, buyerID, p1, 1, key)
	if err != nil || len(state.Lines) != 1 || state.Lines[0].Quantity != 1 || len(e.platform.ItemCheckCalls()) != 1 {
		t.Fatal(state, err)
	}
}
