package cart_test

import (
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

var (
	t0  = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	ttl = 30 * 24 * time.Hour
)

func offer(tenant ids.TenantID, product ids.ProductID, price int64, available int) cart.Offer {
	return cart.Offer{ID: ids.OfferID{Tenant: tenant, Product: product}, Name: "Товар " + string(product),
		Price: money.FromKopecks(price), Available: available, Visible: true}
}

func newCart() *cart.Cart {
	c := cart.New("6f1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b", "7a1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b", t0, ttl)
	c.MarkSaved()

	return cart.Restore(c.State())
}

func TestAddMergesAndChecksStock(t *testing.T) {
	c := newCart()

	if err := c.Add(offer("t1", "p1", 10000, 5), 2, t0, ttl); err != nil {
		t.Fatal(err)
	}
	if err := c.Add(offer("t1", "p1", 10000, 5), 3, t0, ttl); err != nil {
		t.Fatal(err)
	}
	if lines := c.Lines(); len(lines) != 1 || lines[0].Quantity != 5 {
		t.Fatalf("то же предложение складывается: %+v", lines)
	}
	if err := c.Add(offer("t1", "p1", 10000, 5), 1, t0, ttl); !errors.Is(err, cart.ErrNotEnoughStock) {
		t.Fatalf("больше остатка проекции: %v", err)
	}

	hidden := offer("t2", "p1", 100, 10)
	hidden.Visible = false
	if err := c.Add(hidden, 1, t0, ttl); !errors.Is(err, cart.ErrOfferUnavailable) {
		t.Fatalf("скрытое предложение: %v", err)
	}
	for _, quantity := range []int{0, -1, 1000} {
		if err := c.Add(offer("t1", "p2", 1, 2000), quantity, t0, ttl); !errors.Is(err, cart.ErrInvalidQuantity) {
			t.Fatalf("количество %d: %v", quantity, err)
		}
	}
	if c.State().Version != 2 {
		t.Fatalf("версия растёт на единицу за сохранение: %d", c.State().Version)
	}
}

func TestLinesLimit(t *testing.T) {
	c := newCart()

	for i := range cart.MaxLines {
		if err := c.Add(offer("t1", ids.ProductID("p"+string(rune('a'+i%26))+string(rune('a'+i/26))), 1, 10), 1, t0, ttl); err != nil {
			t.Fatal(err)
		}
	}

	if err := c.Add(offer("t1", "overflow", 1, 10), 1, t0, ttl); !errors.Is(err, cart.ErrTooManyLines) {
		t.Fatalf("предел строк: %v", err)
	}
}

func TestSetQuantityAndRemove(t *testing.T) {
	c := newCart()
	p1 := offer("t1", "p1", 10000, 5)
	_ = c.Add(p1, 1, t0, ttl)

	if err := c.SetQuantity(p1, 4, t0, ttl); err != nil || c.Lines()[0].Quantity != 4 {
		t.Fatalf("новое количество: %v %+v", err, c.Lines())
	}
	if err := c.SetQuantity(p1, 6, t0, ttl); !errors.Is(err, cart.ErrNotEnoughStock) {
		t.Fatalf("больше остатка: %v", err)
	}
	if err := c.SetQuantity(offer("t1", "missing", 1, 1), 1, t0, ttl); !errors.Is(err, cart.ErrLineNotFound) {
		t.Fatalf("нет строки: %v", err)
	}
	if err := c.SetQuantity(p1, 0, t0, ttl); err != nil || len(c.Lines()) != 0 {
		t.Fatalf("ноль удаляет строку: %v %+v", err, c.Lines())
	}
	if c.Remove(p1.ID, t0, ttl) {
		t.Fatal("удаление отсутствующей строки — не изменение")
	}
}

func TestApplyChecks(t *testing.T) {
	c := newCart()
	_ = c.Add(offer("t1", "p1", 10000, 5), 2, t0, ttl)
	_ = c.Add(offer("t1", "p2", 5000, 5), 3, t0, ttl)
	_ = c.Add(offer("t2", "p1", 700, 5), 1, t0, ttl)
	_ = c.Add(offer("t2", "p2", 100, 5), 1, t0, ttl)

	changed := c.Apply([]cart.Check{
		{Offer: ids.OfferID{Tenant: "t1", Product: "p1"}, Exists: true, Price: money.FromKopecks(12000), Available: 9},
		{Offer: ids.OfferID{Tenant: "t1", Product: "p2"}, Exists: true, Price: money.FromKopecks(5000), Available: 1},
		{Offer: ids.OfferID{Tenant: "t2", Product: "p1"}, Exists: false},
	}, t0.Add(time.Hour), ttl)
	if !changed {
		t.Fatal("ответы магазинов меняют корзину")
	}

	lines := c.Lines()
	if lines[0].Issue != cart.IssuePriceChanged || lines[0].Price.Kopecks() != 12000 {
		t.Fatalf("новая цена: %+v", lines[0])
	}
	if lines[1].Issue != cart.IssueNotEnoughStock || lines[2].Issue != cart.IssueUnavailable || lines[3].Issue != cart.IssueNone {
		t.Fatalf("пометки: %+v", lines)
	}
	if !c.State().ExpiresAt.Equal(t0.Add(time.Hour + ttl)) {
		t.Fatal("срок жизни продлевается изменением")
	}

	unchanged := c.Apply([]cart.Check{{Offer: ids.OfferID{Tenant: "t2", Product: "p2"}, Exists: true, Price: money.FromKopecks(100), Available: 5}}, t0, ttl)
	if unchanged {
		t.Fatal("тот же ответ — не изменение")
	}
}

func TestGroupsTotalExpiry(t *testing.T) {
	c := newCart()
	_ = c.Add(offer("t2", "p1", 700, 5), 1, t0, ttl)
	_ = c.Add(offer("t1", "p1", 10000, 5), 2, t0, ttl)
	_ = c.Add(offer("t1", "p2", 5000, 5), 3, t0, ttl)

	groups := c.Groups()
	if len(groups) != 2 || groups[0].TenantID != "t1" || len(groups[0].Lines) != 2 || groups[1].TenantID != "t2" {
		t.Fatalf("группы по магазинам: %+v", groups)
	}

	total, err := c.Total()
	if err != nil || total.Kopecks() != 10000*2+5000*3+700 {
		t.Fatalf("сумма: %v %v", total, err)
	}

	if c.ResetIfExpired(t0.Add(ttl-time.Second), ttl) {
		t.Fatal("до срока корзина живёт")
	}
	if !c.ResetIfExpired(t0.Add(ttl), ttl) || len(c.Lines()) != 0 {
		t.Fatal("просроченная корзина очищается")
	}
}
