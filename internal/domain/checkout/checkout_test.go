package checkout_test

import (
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

var t0 = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

const id ids.UUID = "6f1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b"

var selection = checkout.Selection{Carrier: "cdek", PickupPointCode: "MSK1", City: "Город", Address: "ул. Примерная, 1"}

func line(tenant ids.TenantID, product ids.ProductID, quantity int, price int64) checkout.Line {
	return checkout.Line{
		Offer:    ids.OfferID{Tenant: tenant, Product: product},
		Name:     "Товар",
		Quantity: quantity,
		Price:    money.FromKopecks(price),
	}
}

func draft(t *testing.T) *checkout.Checkout {
	t.Helper()

	c, err := checkout.New(
		id,
		"7a1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b",
		"key-00000001",
		[]checkout.Line{line("t2", "p1", 1, 700), line("t1", "p1", 2, 10000), line("t1", "p2", 1, 5000)},
		t0,
	)
	if err != nil {
		t.Fatal(err)
	}
	c.MarkSaved()

	return checkout.Restore(c.State())
}

func available(cost, buyer int64, at time.Time) delivery.Quote {
	return delivery.Quote{
		Status:    delivery.QuoteAvailable,
		Cost:      money.FromKopecks(cost),
		BuyerCost: money.FromKopecks(buyer),
		Subsidy:   money.FromKopecks(cost - buyer),
		MinDays:   2,
		MaxDays:   3,
		ExpiresAt: at,
	}
}

func TestNewGroupsBySeller(t *testing.T) {
	c := draft(t)
	state := c.State()

	if len(state.Groups) != 2 || state.Groups[0].TenantID != "t1" ||
		state.Groups[0].ExternalCheckoutID != string(id)+":t1" {
		t.Fatalf("группы по продавцам с ключом заказа у магазина: %+v", state.Groups)
	}
	if state.Groups[0].Goods.Kopecks() != 25000 || state.GoodsTotal.Kopecks() != 25700 ||
		state.Total.Kopecks() != 25700 ||
		state.Status != checkout.StatusDraft {
		t.Fatalf("суммы: %+v", state)
	}

	if _, err := checkout.New(id, id, "key-00000001", nil, t0); !errors.Is(err, checkout.ErrEmpty) {
		t.Fatalf("пустая корзина: %v", err)
	}
	if _, err := checkout.ParseIdempotencyKey("short"); !errors.Is(err, checkout.ErrInvalidIdempotencyKey) {
		t.Fatalf("короткий ключ: %v", err)
	}
	if key, err := checkout.ParseIdempotencyKey(" 3f0e7c1a-key "); err != nil || key != "3f0e7c1a-key" {
		t.Fatalf("ключ: %q %v", key, err)
	}
}

func TestSelectDeliveryAndConfirm(t *testing.T) {
	c := draft(t)

	if err := c.Confirm(t0); !errors.Is(err, checkout.ErrDeliveryNotSelected) {
		t.Fatalf("без доставки: %v", err)
	}

	partial := map[ids.TenantID]delivery.Quote{"t1": available(30000, 20000, t0.Add(time.Hour))}

	err := c.SelectDelivery(selection, partial, t0)
	if !errors.Is(err, checkout.ErrInvalidQuotes) {
		t.Fatalf("расчёт не у всех продавцов: %v", err)
	}

	quotes := map[ids.TenantID]delivery.Quote{
		"t1": available(30000, 20000, t0.Add(time.Hour)),
		"t2": {Status: delivery.QuoteUnavailable, Reason: "carrier_not_connected", ExpiresAt: t0.Add(time.Hour)},
	}
	if err := c.SelectDelivery(selection, quotes, t0); err != nil {
		t.Fatal(err)
	}
	if state := c.State(); state.DeliveryTotal.Kopecks() != 20000 || state.Total.Kopecks() != 45700 {
		t.Fatalf("итоги с долей покупателя: %+v", state)
	}
	if err := c.Confirm(t0); !errors.Is(err, checkout.ErrDeliveryUnavailable) {
		t.Fatalf("продавец не везёт в пункт: %v", err)
	}
	if out := c.Undeliverable(); len(out) != 1 || out[0] != "t2" {
		t.Fatalf("кто не везёт: %v", out)
	}

	quotes["t2"] = delivery.Unreachable(t0)
	_ = c.SelectDelivery(selection, quotes, t0)
	if need := c.NeedsQuote(t0); len(need) != 1 || need[0] != "t2" || len(c.Undeliverable()) != 0 {
		t.Fatalf("не ответивший магазин пересчитывается: %v", need)
	}

	quotes["t2"] = available(10000, 10000, t0.Add(10*time.Minute))
	if err := c.SelectDelivery(selection, quotes, t0); err != nil {
		t.Fatal(err)
	}
	if err := c.Confirm(t0.Add(11 * time.Minute)); !errors.Is(err, checkout.ErrQuoteExpired) {
		t.Fatalf("устаревший расчёт: %v", err)
	}
	if need := c.NeedsQuote(t0.Add(11 * time.Minute)); len(need) != 1 || need[0] != "t2" {
		t.Fatalf("пересчитать нужно одного продавца: %v", need)
	}

	requoted := map[ids.TenantID]delivery.Quote{"t2": available(10000, 9000, t0.Add(time.Hour))}
	if err := c.Requote(requoted, t0.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := c.Confirm(t0.Add(12 * time.Minute)); err != nil {
		t.Fatal(err)
	}

	state := c.State()
	if state.Status != checkout.StatusConfirmed || state.DeliveryTotal.Kopecks() != 29000 ||
		state.Total.Kopecks() != 54700 ||
		!state.ConfirmedAt.Equal(t0.Add(12*time.Minute)) {
		t.Fatalf("подтверждённая покупка: %+v", state)
	}
	if status, version := c.Origin(); status != checkout.StatusDraft || version != 1 || state.Version != 2 {
		t.Fatalf("исходное состояние для условного перехода: %s %d → %d", status, version, state.Version)
	}

	if err := c.SelectDelivery(selection, quotes, t0); !errors.Is(err, checkout.ErrNotDraft) {
		t.Fatalf("подтверждённую не меняют: %v", err)
	}
	if err := c.Confirm(t0); !errors.Is(err, checkout.ErrNotDraft) {
		t.Fatalf("второе подтверждение: %v", err)
	}
}

func TestReprice(t *testing.T) {
	c := draft(t)
	quotes := map[ids.TenantID]delivery.Quote{
		"t1": available(30000, 30000, t0.Add(time.Hour)),
		"t2": available(10000, 10000, t0.Add(time.Hour)),
	}
	_ = c.SelectDelivery(selection, quotes, t0)

	checks := map[ids.OfferID]checkout.PriceCheck{
		{Tenant: "t1", Product: "p1"}: {Exists: true, Price: money.FromKopecks(11000), Available: 5},
		{Tenant: "t1", Product: "p2"}: {Exists: true, Price: money.FromKopecks(5000), Available: 5},
		{Tenant: "t2", Product: "p1"}: {Exists: true, Price: money.FromKopecks(700), Available: 5},
	}

	changed, err := c.Reprice(checks, t0)
	if err != nil || !changed {
		t.Fatalf("новая цена: %v %v", changed, err)
	}

	state := c.State()
	if state.Groups[0].Goods.Kopecks() != 27000 || state.Groups[0].Quote != nil || state.Groups[1].Quote == nil {
		t.Fatalf("расчёт доставки продавца с новой ценой сброшен: %+v", state.Groups)
	}
	if need := c.NeedsQuote(t0); len(need) != 1 || need[0] != "t1" {
		t.Fatalf("пересчитать продавца с новой ценой: %v", need)
	}

	if changed, err := c.Reprice(checks, t0); err != nil || changed {
		t.Fatalf("те же цены — не изменение: %v %v", changed, err)
	}

	short := map[ids.OfferID]checkout.PriceCheck{
		{Tenant: "t1", Product: "p1"}: {Exists: true, Price: money.FromKopecks(11000), Available: 1},
		{Tenant: "t1", Product: "p2"}: {Exists: false},
	}
	if _, err := c.Reprice(short, t0); !errors.Is(err, checkout.ErrOfferUnavailable) {
		t.Fatalf("нехватка и снятые с продажи: %v", err)
	}
}
