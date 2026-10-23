package checkout_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

var t0 = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

const buyerID ids.UUID = "7a1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b"

const key = "checkout-key-0001"

var (
	p1 = ids.OfferID{Tenant: "t1", Product: "p1"}
	p2 = ids.OfferID{Tenant: "t2", Product: "p1"}
)

type env struct {
	store    *fake.Store
	index    *fake.Index
	platform *fake.Platform
	clock    *fake.Clock
	carts    *appcart.Service
	service  *appcheckout.Service
}

var w3ServiceDelivery = participant.DeliverySettings{Regions: []string{"cdek:r1"}, Carriers: []string{"cdek"}}
var w3ServiceSubsidy = participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed}

func setup(t *testing.T) *env {
	t.Helper()

	e := &env{store: fake.NewStore(), index: fake.NewIndex(), platform: fake.NewPlatform(), clock: fake.NewClock(t0)}
	identities := &fake.IDs{}
	pickups := fake.NewPickupIndex()
	provider := &fake.PickupProvider{Info: delivery.CarrierInfo{Code: "cdek", Name: "СДЭК"}}
	directory := appdelivery.NewDirectory(
		e.store,
		[]ports.PickupPointsProvider{provider},
		pickups,
		e.store,
		e.clock,
		"pod-1",
	)

	e.carts = appcart.NewService(e.store, e.index, e.platform, identities, e.clock, 30*24*time.Hour)
	e.service = appcheckout.NewService(
		e.store,
		e.index,
		e.platform,
		directory,
		identities,
		e.clock,
		appcheckout.Config{
			QuoteTTL:      30 * time.Minute,
			QuoteTimeout:  time.Second,
			ReturnBaseURL: "https://market.example",
		},
	)

	ctx := context.Background()

	err := e.index.Put(ctx, []ports.OfferDocument{
		{
			ID:                p1.String(),
			TenantID:          "t1",
			ProductID:         "p1",
			Name:              "Чайник",
			Price:             199000,
			Available:         3,
			Visible:           true,
			Listed:            true,
			CheckoutAvailable: true,
			Version:           1,
		},
		{
			ID:                p2.String(),
			TenantID:          "t2",
			ProductID:         "p1",
			Name:              "Кружка",
			Price:             35000,
			Available:         10,
			Visible:           true,
			Listed:            true,
			CheckoutAvailable: true,
			Version:           1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	point, err := delivery.NewPickupPoint(
		"cdek",
		"MSK1",
		"Пункт",
		"ул. Примерная, 1",
		"Город",
		delivery.Location{Lat: 55.7, Lon: 37.6},
		"",
		nil,
		t0,
	)
	if err != nil {
		t.Fatal(err)
	}
	point.RegionCode = "r1"
	e.store.SeedPickupPoint(point)

	e.store.SeedParticipant(participant.State{
		TenantID:         "t1",
		Delivery:         w3ServiceDelivery,
		FreeDeliveryFrom: money.FromKopecks(1000000),
		Slug:             "s1",
		Name:             "Первый",
		Status:           participant.StatusConnected,
		Enabled:          true,
		ShopEnabled:      true,
		Subsidy: participant.Subsidy{
			Enabled: true,
			Type:    participant.SubsidyPercent,
			Value:   50,
		},
		Version:   1,
		CreatedAt: t0,
		UpdatedAt: t0,
	})

	e.platform.OrderNow = t0
	e.store.SeedParticipant(participant.State{
		TenantID:         "t2",
		ShopEnabled:      true,
		Enabled:          true,
		Delivery:         w3ServiceDelivery,
		Subsidy:          w3ServiceSubsidy,
		FreeDeliveryFrom: money.FromKopecks(1000000),
	})
	e.platform.SetItem(
		"t1",
		ports.ItemCheck{ProductID: "p1", Exists: true, Price: money.FromKopecks(199000), Available: 3},
	)
	e.platform.SetItem(
		"t2",
		ports.ItemCheck{ProductID: "p1", Exists: true, Price: money.FromKopecks(35000), Available: 10},
	)
	e.platform.SetQuote("t1", delivery.Offered{Available: true, Cost: money.FromKopecks(30000), MinDays: 2, MaxDays: 4})
	e.platform.SetQuote(
		"t2",
		delivery.Offered{
			Available: true,
			Cost:      money.FromKopecks(25000),
			MinDays:   3,
			MaxDays:   5,
			ExpiresAt: t0.Add(10 * time.Minute),
		},
	)

	if _, err := e.carts.Add(ctx, buyerID, p1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.carts.Add(ctx, buyerID, p2, 1); err != nil {
		t.Fatal(err)
	}

	return e
}

func TestCreateIsIdempotent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	first, err := e.service.Create(ctx, buyerID, key)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != checkout.StatusDraft || len(first.Groups) != 2 || first.GoodsTotal.Kopecks() != 2*199000+35000 {
		t.Fatalf("покупка из корзины: %+v", first)
	}
	if first.Groups[0].ExternalCheckoutID != string(first.ID)+":t1" {
		t.Fatalf("ключ заказа у магазина: %s", first.Groups[0].ExternalCheckoutID)
	}

	second, err := e.service.Create(ctx, buyerID, key)
	if err != nil || second.ID != first.ID {
		t.Fatalf("тот же ключ — та же покупка: %s %s %v", second.ID, first.ID, err)
	}

	if _, err := e.service.Create(ctx, buyerID, "bad"); !errors.Is(err, checkout.ErrInvalidIdempotencyKey) {
		t.Fatalf("ключ не по формату: %v", err)
	}

	_, err = e.service.Create(ctx, "8a1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b", key)
	if !errors.Is(err, checkout.ErrEmpty) {
		t.Fatalf("без корзины: %v", err)
	}

	_, err = e.service.Get(ctx, "8a1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b", first.ID)
	if !errors.Is(err, checkout.ErrNotFound) {
		t.Fatalf("чужую покупку не видно: %v", err)
	}
}

func TestCreateRefusesUnpayableSeller(t *testing.T) {
	e := setup(t)

	_ = e.index.Put(context.Background(), []ports.OfferDocument{
		{
			ID:                p2.String(),
			TenantID:          "t2",
			ProductID:         "p1",
			Name:              "Кружка",
			Price:             35000,
			Available:         10,
			Visible:           true,
			Listed:            true,
			CheckoutAvailable: false,
			Version:           2,
		},
	})

	if _, err := e.service.Create(context.Background(), buyerID, key); !errors.Is(err, checkout.ErrOfferUnavailable) {
		t.Fatalf("продавец без оплаты через маркетплейс в покупку не попадает: %v", err)
	}
}

func TestQuote(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	created, _ := e.service.Create(ctx, buyerID, key)

	state, err := e.service.Quote(ctx, buyerID, created.ID, "cdek", "MSK1")
	if err != nil {
		t.Fatal(err)
	}

	t1, t2 := state.Groups[0].Quote, state.Groups[1].Quote
	if t1.Status != delivery.QuoteAvailable || t1.Subsidy.Kopecks() != 15000 || t1.BuyerCost.Kopecks() != 15000 {
		t.Fatalf("субсидия продавца — половина доставки: %+v", t1)
	}
	if t2.Subsidy.Kopecks() != 0 || !t2.ExpiresAt.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("без участника — без субсидии, срок магазина короче своего: %+v", t2)
	}
	if state.DeliveryTotal.Kopecks() != 15000+25000 || state.Delivery.City != "Город" {
		t.Fatalf("итог доставки и выбранный пункт: %+v", state)
	}

	request := e.platform.QuoteRequests()[0]
	if request.Carrier != "cdek" || request.PickupPointCode != "MSK1" || request.City != "Город" ||
		len(request.Items) != 1 {
		t.Fatalf("запрос магазину: %+v", request)
	}

	_, err = e.service.Quote(ctx, buyerID, created.ID, "cdek", "NOPE")
	if !errors.Is(err, delivery.ErrPickupPointNotFound) {
		t.Fatalf("неизвестный пункт: %v", err)
	}
}

func TestQuoteMarksSellersWithoutCarrier(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	created, _ := e.service.Create(ctx, buyerID, key)

	e.platform.SetQuote("t2", delivery.Offered{Reason: "carrier_not_connected"})
	e.platform.Fail("t1", errors.New("тайм-аут"))

	state, err := e.service.Quote(ctx, buyerID, created.ID, "cdek", "MSK1")
	if err != nil {
		t.Fatal(err)
	}
	if q := state.Groups[0].Quote; q.Status != delivery.QuoteUnavailable ||
		q.Reason != delivery.ReasonSellerUnreachable {
		t.Fatalf("магазин не ответил: %+v", q)
	}
	if q := state.Groups[1].Quote; q.Status != delivery.QuoteUnavailable || q.Reason != "carrier_not_connected" {
		t.Fatalf("продавец без перевозчика: %+v", q)
	}

	if _, err := e.service.Confirm(ctx, buyerID, created.ID); !errors.Is(err, checkout.ErrDeliveryUnavailable) {
		t.Fatalf("подтвердить нельзя: %v", err)
	}
}

func TestConfirm(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	created, _ := e.service.Create(ctx, buyerID, key)

	if _, err := e.service.Confirm(ctx, buyerID, created.ID); !errors.Is(err, checkout.ErrDeliveryNotSelected) {
		t.Fatalf("без доставки: %v", err)
	}

	if _, err := e.service.Quote(ctx, buyerID, created.ID, "cdek", "MSK1"); err != nil {
		t.Fatal(err)
	}

	e.clock.Advance(15 * time.Minute)
	e.platform.SetQuote("t2", delivery.Offered{Available: true, Cost: money.FromKopecks(26000), MinDays: 3, MaxDays: 5})

	state, err := e.service.Confirm(ctx, buyerID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != checkout.StatusConfirmed || state.Groups[1].Quote.Cost.Kopecks() != 26000 ||
		state.Total.Kopecks() != 2*199000+35000+15000+26000 {
		t.Fatalf("устаревший расчёт пересчитан, покупка подтверждена: %+v", state)
	}

	again, err := e.service.Confirm(ctx, buyerID, created.ID)
	if err != nil || again.Version != state.Version {
		t.Fatalf("повтор подтверждения отдаёт ту же покупку: %v", err)
	}
	if _, err := e.service.Quote(ctx, buyerID, created.ID, "cdek", "MSK1"); !errors.Is(err, checkout.ErrNotDraft) {
		t.Fatalf("подтверждённую не пересчитывают: %v", err)
	}
}

func TestConfirmReportsPriceChange(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	created, _ := e.service.Create(ctx, buyerID, key)
	_, _ = e.service.Quote(ctx, buyerID, created.ID, "cdek", "MSK1")

	e.platform.SetItem(
		"t1",
		ports.ItemCheck{ProductID: "p1", Exists: true, Price: money.FromKopecks(209000), Available: 3},
	)

	state, err := e.service.Confirm(ctx, buyerID, created.ID)
	if !errors.Is(err, checkout.ErrChanged) || state.Status != checkout.StatusDraft ||
		state.Groups[0].Lines[0].Price.Kopecks() != 209000 {
		t.Fatalf("новая цена записана, покупка ждёт подтверждения: %+v %v", state, err)
	}
	if state.Groups[0].Quote != nil {
		t.Fatal("расчёт продавца с новой ценой сброшен")
	}

	confirmed, err := e.service.Confirm(ctx, buyerID, created.ID)
	if err != nil || confirmed.Status != checkout.StatusConfirmed || confirmed.Groups[0].Quote == nil {
		t.Fatalf("повторное подтверждение пересчитывает доставку и проходит: %+v %v", confirmed, err)
	}
}

func TestConfirmRefusesMissingStock(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	created, _ := e.service.Create(ctx, buyerID, key)
	_, _ = e.service.Quote(ctx, buyerID, created.ID, "cdek", "MSK1")

	e.platform.SetItem(
		"t1",
		ports.ItemCheck{ProductID: "p1", Exists: true, Price: money.FromKopecks(199000), Available: 1},
	)

	if _, err := e.service.Confirm(ctx, buyerID, created.ID); !errors.Is(err, checkout.ErrOfferUnavailable) {
		t.Fatalf("нехватка у магазина: %v", err)
	}

	e.platform.Fail("t2", errors.New("недоступен"))
	if _, err := e.service.Confirm(ctx, buyerID, created.ID); !errors.Is(err, appcart.ErrSellerUnreachable) {
		t.Fatalf("магазин не ответил на перепроверку: %v", err)
	}
}
