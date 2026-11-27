package cart_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

var t0 = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

const buyerID ids.UUID = "7a1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b"

var (
	p1 = ids.OfferID{Tenant: "t1", Product: "p1"}
	p2 = ids.OfferID{Tenant: "t2", Product: "p1"}
)

type env struct {
	store    *fake.Store
	platform *fake.Platform
	clock    *fake.Clock
	service  *appcart.Service
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{store: fake.NewStore(), platform: fake.NewPlatform(), clock: fake.NewClock(t0)}
	e.service = appcart.NewService(e.store, e.platform, &fake.IDs{}, e.clock, 30*24*time.Hour, "test-cart-key")
	// Корзина читает предложение из проекции каталога; поиск ей не нужен.
	e.store.SeedBuyerOffer(p1, "Чайник", money.FromKopecks(199000))
	e.store.SeedBuyerOffer(p2, "Кружка", money.FromKopecks(35000))
	e.store.SeedProduct(catalog.ProductState{TenantID: "t3", ProductID: "p1"})

	e.platform.SetItem("t1", ports.ItemCheck{
		ProductID: "p1",
		Exists:    true,
		Price:     money.FromKopecks(199000),
		Available: 3,
	})
	e.platform.SetItem("t2", ports.ItemCheck{
		ProductID: "p1",
		Exists:    true,
		Price:     money.FromKopecks(35000),
		Available: 10,
	})
	return e
}

func TestAddChangeRemove(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	if state, err := e.service.Get(ctx, buyerID); err != nil || len(state.Lines) != 0 {
		t.Fatalf("пустая корзина: %+v %v", state, err)
	}
	if _, ok := e.store.Cart(buyerID); ok {
		t.Fatal("чтение пустой корзины её не заводит")
	}

	state, err := e.seedCartLine(ctx, p1, 2)
	if err != nil || len(state.Lines) != 1 || state.Lines[0].Price.Kopecks() != 199000 ||
		state.Lines[0].Name != "Чайник" {
		t.Fatalf("добавление со снимком цены индекса: %+v %v", state, err)
	}

	if _, err := e.seedCartLine(ctx, p1, 2); !errors.Is(err, cart.ErrNotEnoughStock) {
		t.Fatalf("сверх остатка проекции: %v", err)
	}

	_, err = e.seedCartLine(ctx, ids.OfferID{Tenant: "t3", Product: "p1"}, 1)
	if !errors.Is(err, cart.ErrOfferUnavailable) {
		t.Fatalf("скрытое предложение: %v", err)
	}

	_, err = e.seedCartLine(ctx, ids.OfferID{Tenant: "t9", Product: "p9"}, 1)
	if !errors.Is(err, cart.ErrOfferUnavailable) {
		t.Fatalf("нет предложения: %v", err)
	}

	if state, err = e.service.SetQuantity(ctx, buyerID, p1, 3); err != nil || state.Lines[0].Quantity != 3 {
		t.Fatalf("количество: %+v %v", state, err)
	}
	if state, err = e.seedCartLine(ctx, p2, 1); err != nil || len(state.Lines) != 2 {
		t.Fatalf("второй магазин: %+v %v", state, err)
	}
	if state, err = e.service.SetQuantity(ctx, buyerID, p2, 0); err != nil || len(state.Lines) != 1 {
		t.Fatalf("ноль убирает строку: %+v %v", state, err)
	}
	if state, err = e.service.Remove(ctx, buyerID, p1); err != nil || len(state.Lines) != 0 {
		t.Fatalf("удаление: %+v %v", state, err)
	}
}

func TestExpiredCartResets(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	if _, err := e.seedCartLine(ctx, p1, 1); err != nil {
		t.Fatal(err)
	}

	e.clock.Advance(31 * 24 * time.Hour)

	state, err := e.service.Get(ctx, buyerID)
	if err != nil || len(state.Lines) != 0 {
		t.Fatalf("просроченная корзина пуста: %+v %v", state, err)
	}
	if stored, _ := e.store.Cart(buyerID); len(stored.Lines) != 0 {
		t.Fatal("очищенная корзина сохранена")
	}
}

func TestVerify(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	_, _ = e.seedCartLine(ctx, p1, 2)
	_, _ = e.seedCartLine(ctx, p2, 1)

	e.platform.SetItem(
		"t1",
		ports.ItemCheck{ProductID: "p1", Exists: true, Price: money.FromKopecks(209000), Available: 5},
	)

	e.platform.SetItem("t2", ports.ItemCheck{ProductID: "p1", Exists: false})
	state, err := e.service.Verify(ctx, buyerID)
	if err != nil {
		t.Fatal(err)
	}
	byOffer := map[ids.OfferID]cart.Line{}
	for _, line := range state.Lines {
		byOffer[line.Offer] = line
	}

	if line := byOffer[p1]; line.Issue != cart.IssuePriceChanged || line.Price.Kopecks() != 209000 {
		t.Fatalf("новая цена магазина: %+v", line)
	}
	if line := byOffer[p2]; line.Issue != cart.IssueUnavailable {
		t.Fatalf("магазин больше не продаёт: %+v", line)
	}

	e.platform.Fail("t2", errors.New("сервер недоступен"))
	if _, err := e.service.Verify(ctx, buyerID); !errors.Is(err, appcart.ErrSellerUnreachable) ||
		errs.CodeOf(err) != errs.Unavailable {
		t.Fatalf("магазин не ответил: %v", err)
	}
}

func (e *env) seedCartLine(ctx context.Context, offer ids.OfferID, quantity int) (cart.State, error) {
	return e.service.AddOnce(ctx, buyerID, offer, quantity, e.service.NewAddKey(buyerID))
}
