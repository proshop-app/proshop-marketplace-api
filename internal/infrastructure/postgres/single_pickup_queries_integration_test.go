//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type selectionPoint struct {
	calls int
}

func (p *selectionPoint) Point(
	_ context.Context, _ ids.UUID, carrier delivery.Carrier, code, city string,
) (delivery.PickupPoint, error) {
	p.calls++
	return delivery.PickupPoint{
		Carrier:    carrier,
		Code:       code,
		City:       city,
		RegionCode: "RU-MOW",
		Address:    "Адрес",
	}, nil
}

func TestSinglePickupSelectionSQL(t *testing.T) {
	for _, count := range []int{1, 2, 5} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
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
			lines := []checkout.Line{}
			platform := fake.NewPlatform()
			for i := range count {
				tenant := ids.TenantID(fmt.Sprintf("t%d", i+1))
				lines = append(lines, checkout.Line{
					Offer:    ids.OfferID{Tenant: tenant, Product: "p1"},
					Name:     "Товар",
					Quantity: 1,
					Price:    money.FromKopecks(10000),
				})
				platform.SetQuote(tenant, delivery.Offered{
					Available: true,
					MinDays:   2,
					MaxDays:   4,
					Cost:      money.FromKopecks(10000),
				})
			}
			clock := fake.NewClock(at(0))
			id := ids.UUID("10000000-0000-4000-8000-000000000001")
			c, err := checkout.New(id, buyerA, "single-pickup-sql", lines, clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				return tx.Checkouts().Add(ctx, c)
			}); err != nil {
				t.Fatal(err)
			}
			points := &selectionPoint{}
			service := appcheckout.NewService(uow, platform, points, nil, &fake.IDs{}, clock, appcheckout.Config{
				QuoteTTL:     time.Minute,
				QuoteTimeout: time.Second,
			})
			counter.reset()
			selected, err := service.Quote(ctx, buyerA, c.ID(), "cdek", "P1", "Город")
			if err != nil || len(selected.Groups) != count ||
				len(platform.QuoteRequests()) != count || points.calls != 1 {
				t.Fatal(selected, err, platform.QuoteRequests(), points.calls)
			}
			counter.log(t, fmt.Sprintf("сценарий, магазинов=%d", count))
			if _, err := service.Sellers(ctx, []checkout.State{selected}); err != nil {
				t.Fatal(err)
			}
			counter.log(t, fmt.Sprintf("с ответом HTTP, магазинов=%d", count))
			t.Logf("магазинов=%d расчетов=%d проверокПВЗ=%d", count, len(platform.QuoteRequests()), points.calls)
		})
	}
}

func TestCartReadSQLIncludesAvailabilityWithoutExtraQuery(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	config := pool.Config()
	counter := &checkoutQueryCounter{}
	counter.reset()
	config.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	uow := postgres.NewUnitOfWork(traced)
	seedBuyer(t, uow, buyerA, "79251234567")
	clock := fake.NewClock(at(0))
	basket := cart.New("20000000-0000-4000-8000-000000000001", buyerA, clock.Now(), time.Hour)
	offer := cart.Offer{
		ID:        ids.OfferID{Tenant: "t1", Product: "p1"},
		Name:      "Товар",
		Visible:   true,
		Available: 1,
		Price:     money.FromKopecks(10000),
	}
	if err := basket.Add(offer, 1, clock.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Carts().Save(ctx, basket)
	}); err != nil {
		t.Fatal(err)
	}
	service := appcart.NewService(uow, fake.NewPlatform(), &fake.IDs{}, clock, time.Hour, "test-cart-key")
	counter.reset()
	current, err := service.Get(ctx, buyerA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Sellers(ctx, current); err != nil {
		t.Fatal(err)
	}
	counter.log(t, "GET корзины с ответом")
}
