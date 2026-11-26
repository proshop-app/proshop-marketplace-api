//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

type checkoutQueryCounter struct {
	mu     sync.Mutex
	counts map[string]int
	// keep — запоминать тексты команд для проверки их вида, не только числа.
	keep bool
	sqls []string
}

func (c *checkoutQueryCounter) TraceQueryStart(
	ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData,
) context.Context {
	words := strings.Fields(data.SQL)
	if len(words) > 0 {
		c.mu.Lock()
		c.counts[strings.ToUpper(words[0])]++
		if c.keep {
			c.sqls = append(c.sqls, data.SQL)
		}
		c.mu.Unlock()
	}
	return ctx
}

func (c *checkoutQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *checkoutQueryCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts = make(map[string]int)
	c.sqls = nil
}

func (c *checkoutQueryCounter) remember(keep bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keep = keep
}

func (c *checkoutQueryCounter) statements() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sqls...)
}

func (c *checkoutQueryCounter) log(t *testing.T, action string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, count := range c.counts {
		total += count
	}
	t.Logf(
		"SQL %s: total=%d SELECT=%d INSERT=%d UPDATE=%d DELETE=%d BEGIN=%d COMMIT=%d other=%v",
		action,
		total,
		c.counts["SELECT"],
		c.counts["INSERT"],
		c.counts["UPDATE"],
		c.counts["DELETE"],
		c.counts["BEGIN"],
		c.counts["COMMIT"],
		c.counts,
	)
}

// Команды считаются трассировкой настоящего pgx-соединения, включая BEGIN/COMMIT.
// Два магазина, свежий тариф, покупатель уже выбрал ПВЗ; фоновые обработчики не входят в действия.
func TestCheckoutBuyerActionSQL(t *testing.T) {
	pool := testPool(t)
	config := pool.Config()
	counter := &checkoutQueryCounter{}
	counter.reset()
	config.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	uow := postgres.NewUnitOfWork(traced)
	seedBuyer(t, uow, buyerA, "79251234567")
	clock, platform := fake.NewClock(at(0)), fake.NewPlatform()
	platform.OrderNow = clock.Now()
	lines := []checkout.Line{}
	for _, tenant := range []ids.TenantID{"t1", "t2"} {
		seedRecommendationProducts(t, uow, tenant, 1)
		lines = append(
			lines,
			checkout.Line{
				Offer:    ids.OfferID{Tenant: tenant, Product: "p1"},
				Name:     "Товар",
				Quantity: 1,
				Price:    money.FromKopecks(10000),
			},
		)
		platform.SetItem(
			tenant,
			ports.ItemCheck{
				ProductID: "p1",
				Exists:    true,
				Available: 1,
				Price:     money.FromKopecks(10000),
			},
		)
	}
	c, err := checkout.New(
		"10000000-0000-4000-8000-000000000001",
		buyerA,
		"sql-buyer-actions",
		lines,
		clock.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	err = c.SelectDelivery(
		checkout.Selection{
			Carrier:         "cdek",
			PickupPointCode: "P1",
			City:            "Город",
			RegionCode:      "RU-MOW",
		},
		map[ids.TenantID]delivery.Quote{
			"t1": {Status: delivery.QuoteAvailable, ExpiresAt: clock.Now().Add(time.Hour)},
			"t2": {Status: delivery.QuoteAvailable, ExpiresAt: clock.Now().Add(time.Hour)},
		},
		clock.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := uow.Do(
		ctx,
		func(ctx context.Context, tx ports.Tx) error {
			return tx.Checkouts().Add(ctx, c)
		},
	); err != nil {
		t.Fatal(err)
	}
	service := appcheckout.NewService(
		uow,
		platform,
		nil,
		nil,
		&fake.IDs{},
		clock,
		appcheckout.Config{
			PaymentStub: true,
		},
	)
	counter.reset()
	confirmed, err := service.Confirm(ctx, buyerA, c.ID())
	if err != nil || confirmed.Status != checkout.StatusConfirmed {
		t.Fatal(confirmed, err)
	}
	counter.log(t, "confirm")
	if err := uow.Do(
		ctx,
		func(ctx context.Context, tx ports.Tx) error {
			current, err := tx.Checkouts().Get(ctx, c.ID())
			if err != nil {
				return err
			}
			if _, err := current.BeginOrders(
				checkout.Contact{
					Name:  "Покупатель",
					Phone: "79251234567",
				},
				clock.Now(),
			); err != nil {
				return err
			}
			for _, group := range current.Groups() {
				order, err := platform.CreateOrder(
					ctx,
					group.TenantID,
					ports.CreateOrderRequest{
						PaymentDueAt:       group.ExpiresAt,
						Carrier:            string(current.Delivery().Carrier),
						PickupPointCode:    current.Delivery().PickupPointCode,
						ExternalCheckoutID: group.ExternalCheckoutID,
						GroupID:            group.TenantID,
						Items:              []ports.OrderItem{{ProductID: "p1", Quantity: 1, Price: group.Goods}},
					},
				)
				if err != nil {
					return err
				}
				if _, err := current.AttachOrder(group.TenantID, order, clock.Now()); err != nil {
					return err
				}
			}
			if _, err := current.MarkCartConsumed(clock.Now()); err != nil {
				return err
			}
			return tx.Checkouts().Update(ctx, current)
		},
	); err != nil {
		t.Fatal(err)
	}
	counter.reset()
	if _, err := service.Get(ctx, buyerA, c.ID()); err != nil {
		t.Fatal(err)
	}
	counter.log(t, "get")
	counter.reset()
	if _, err := service.Pay(ctx, buyerA, c.ID()); err != nil {
		t.Fatal(err)
	}
	counter.log(t, "pay-open")
	counter.reset()
	removed, err := service.RemoveGroup(ctx, buyerA, c.ID(), "t1")
	if err != nil || !removed.Groups[0].Excluded() || removed.Total.Kopecks() != 10000 {
		t.Fatal(removed, err)
	}
	counter.log(t, "remove")
	if _, err := service.Confirm(ctx, buyerA, c.ID()); err != nil {
		t.Fatal(err)
	}
	contact := checkout.Contact{
		Name:  "Покупатель",
		Phone: "79251234567",
	}
	if _, err := service.CreateOrders(ctx, buyerA, c.ID(), contact); err != nil {
		t.Fatal(err)
	}
	counter.reset()
	if err := service.ConfirmPayment(ctx, buyerA, c.ID()); err != nil {
		t.Fatal(err)
	}
	counter.log(t, "pay-confirm-one-shop")

}
