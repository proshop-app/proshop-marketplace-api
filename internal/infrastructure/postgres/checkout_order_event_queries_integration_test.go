//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// orderEventStatements — BEGIN, отметка события, чтение покупки и групп, запись покупки и групп, COMMIT.
const orderEventStatements = 7

// orderEventShops — магазины покупки: цена события не должна расти с их числом.
var orderEventShops = []ids.TenantID{"t1", "t2", "t3", "t4"}

type orderEventBench struct {
	counter  *checkoutQueryCounter
	pool     *pgxpool.Pool
	uow      *postgres.UnitOfWork
	service  *appcheckout.Service
	platform *fake.Platform
	clock    *fake.Clock
}

// Цена одного события статуса — SQL-команды трассировки pgx, включая BEGIN/COMMIT. Покупка из
// четырёх магазинов: событие меняет одну группу, остальные группы не переписываются.
func TestOrderEventSQL(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	id, orders := bench.paidPurchase(t, 1)
	bench.counter.reset()
	if err := bench.service.ApplyOrderEvent(ctx, assembledEvent(id, "t1", orders["t1"], 1)); err != nil {
		t.Fatal(err)
	}
	bench.counter.log(t, "order-event")
	if got := bench.counter.total(); got != orderEventStatements {
		t.Fatalf("событие статуса: SQL %d, ожидалось %d", got, orderEventStatements)
	}
	if got := bench.counter.counts["UPDATE"]; got != 2 {
		t.Fatalf("событие переписывает покупку и одну строку групп: UPDATE %d, ожидалось 2", got)
	}
}

// События разных магазинов одной покупки идут в разных партициях и на разных репликах: они
// ждут друг друга на строке покупки, а не конфликтуют версией и не уходят в повтор приёма.
func TestConcurrentOrderEventsOfOnePurchaseDoNotConflict(t *testing.T) {
	bench := newOrderEventBench(t)
	ctx := context.Background()
	for round := range 5 {
		id, orders := bench.paidPurchase(t, round+2)
		start := make(chan struct{})
		failures := make(chan error, len(orderEventShops))
		var wg sync.WaitGroup
		for i, tenant := range orderEventShops {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				failures <- bench.service.ApplyOrderEvent(ctx, assembledEvent(id, tenant, orders[tenant], i))
			}()
		}
		close(start)
		wg.Wait()
		close(failures)
		for err := range failures {
			if err != nil {
				t.Fatalf("круг %d: событие соседнего магазина не должно давать конфликт: %v", round, err)
			}
		}
		state, err := bench.service.Get(ctx, buyerA, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range state.Groups {
			if g.Status != checkout.GroupAssembled {
				t.Fatalf("круг %d: группа %s не применила событие: %s", round, g.TenantID, g.Status)
			}
		}
	}
}

func newOrderEventBench(t *testing.T) *orderEventBench {
	t.Helper()
	pool := testPool(t)
	config := pool.Config()
	counter := &checkoutQueryCounter{}
	counter.reset()
	config.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	uow := postgres.NewUnitOfWork(traced)
	seedBuyer(t, uow, buyerA, "79251234567")
	clock, platform := fake.NewClock(at(0)), fake.NewPlatform()
	platform.OrderNow = clock.Now()
	for _, tenant := range orderEventShops {
		seedRecommendationProducts(t, uow, tenant, 1)
		platform.SetItem(
			tenant,
			ports.ItemCheck{ProductID: "p1", Exists: true, Available: 100, Price: money.FromKopecks(10000)},
		)
	}
	service := appcheckout.NewService(
		uow,
		platform,
		nil,
		nil,
		&fake.IDs{},
		clock,
		appcheckout.Config{PaymentStub: true},
	)
	return &orderEventBench{counter: counter, pool: traced, uow: uow, service: service, platform: platform, clock: clock}
}

// paidPurchase — оплаченная покупка всех магазинов стенда; номера заказов по магазину.
func (b *orderEventBench) paidPurchase(t *testing.T, n int) (ids.UUID, map[ids.TenantID]string) {
	t.Helper()
	ctx := context.Background()
	c := b.orderedPurchase(t, n)
	if _, err := b.service.Pay(ctx, buyerA, c.ID()); err != nil {
		t.Fatal(err)
	}
	if err := b.service.ConfirmPayment(ctx, buyerA, c.ID()); err != nil {
		t.Fatal(err)
	}
	state, err := b.service.Get(ctx, buyerA, c.ID())
	if err != nil {
		t.Fatal(err)
	}
	orders := map[ids.TenantID]string{}
	for _, g := range state.Groups {
		if g.Status != checkout.GroupPaid {
			t.Fatalf("группа %s не оплачена: %s", g.TenantID, g.Status)
		}
		orders[g.TenantID] = g.OrderID
	}
	return c.ID(), orders
}

func (b *orderEventBench) orderedPurchase(t *testing.T, n int) *checkout.Checkout {
	t.Helper()
	ctx := context.Background()
	lines := make([]checkout.Line, 0, len(orderEventShops))
	quotes := map[ids.TenantID]delivery.Quote{}
	for _, tenant := range orderEventShops {
		lines = append(lines, checkout.Line{
			Offer:    ids.OfferID{Tenant: tenant, Product: "p1"},
			Name:     "Товар",
			Quantity: 1,
			Price:    money.FromKopecks(10000),
		})
		quotes[tenant] = delivery.Quote{Status: delivery.QuoteAvailable, ExpiresAt: b.clock.Now().Add(time.Hour)}
	}
	id := ids.UUID(fmt.Sprintf("20000000-0000-4000-8000-%012d", n))
	c, err := checkout.New(id, buyerA, fmt.Sprintf("order-event-%d", n), lines, b.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	selection := checkout.Selection{Carrier: "cdek", PickupPointCode: "P1", City: "Город", RegionCode: "RU-MOW"}
	if err := c.SelectDelivery(selection, quotes, b.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := c.Confirm(b.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BeginOrders(checkout.Contact{Name: "Покупатель", Phone: "79251234567"}, b.clock.Now()); err != nil {
		t.Fatal(err)
	}
	for _, group := range c.Groups() {
		order, err := b.platform.CreateOrder(ctx, group.TenantID, ports.CreateOrderRequest{
			PaymentDueAt:       group.ExpiresAt,
			Carrier:            string(c.Delivery().Carrier),
			PickupPointCode:    c.Delivery().PickupPointCode,
			ExternalCheckoutID: group.ExternalCheckoutID,
			GroupID:            group.TenantID,
			Items:              []ports.OrderItem{{ProductID: "p1", Quantity: 1, Price: group.Goods}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.AttachOrder(group.TenantID, order, b.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.MarkCartConsumed(b.clock.Now()); err != nil {
		t.Fatal(err)
	}
	add := func(ctx context.Context, tx ports.Tx) error { return tx.Checkouts().Add(ctx, c) }
	if err := b.uow.Do(ctx, add); err != nil {
		t.Fatal(err)
	}
	return c
}

func assembledEvent(id ids.UUID, tenant ids.TenantID, orderID string, n int) appcheckout.OrderEvent {
	return appcheckout.OrderEvent{
		EventID:    fmt.Sprintf("orders:%s:%s:%d", id, tenant, n),
		TenantID:   tenant,
		CheckoutID: id,
		Order:      checkout.SellerOrder{OrderID: orderID, Status: checkout.GroupAssembled, Revision: 10},
	}
}

func (c *checkoutQueryCounter) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, count := range c.counts {
		total += count
	}
	return total
}
