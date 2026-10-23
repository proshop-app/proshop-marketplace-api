//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	buyerA ids.UUID = "6f1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b"
	buyerB ids.UUID = "7a1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b"
)

func do(t *testing.T, uow *postgres.UnitOfWork, fn func(ctx context.Context, tx ports.Tx) error) error {
	t.Helper()

	return uow.Do(context.Background(), fn)
}

func seedBuyer(t *testing.T, uow *postgres.UnitOfWork, id ids.UUID, phone buyer.Phone) {
	t.Helper()

	err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Buyers().Add(ctx, buyer.New(id, phone, at(0)))
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestParticipantShopSettings(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	seedTenant(t, uow, "t1", "shop-one", participant.TenantActive)

	subsidy := participant.Subsidy{
		Enabled:   true,
		Type:      participant.SubsidyPercent,
		Value:     30,
		MaxAmount: money.FromKopecks(50000),
	}

	err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		tenant, _ := tx.Registry().Get(ctx, "t1")
		p, _ := participant.New(tenant, "Магазин", at(0))
		if err := tx.Participants().Add(ctx, p); err != nil {
			return err
		}

		if _, err := p.ApplyShopSettings(true, subsidy, at(1)); err != nil {
			return err
		}

		return tx.Participants().Update(ctx, p)
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, "t1")
		if err != nil || !p.ShopEnabled() || p.Subsidy() != subsidy || !p.State().SettingsSyncedAt.Equal(at(1)) {
			t.Fatalf("настройки магазина после хранения: %+v %v", p.State(), err)
		}

		state, err := tx.Offers().ParticipantOffers(ctx, ports.OfferKey{}, 10)
		if err != nil || len(state) != 0 {
			t.Fatalf("модель чтения читает новые колонки: %v", err)
		}

		return nil
	})
}

func TestBuyersAndChallenges(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	seedBuyer(t, uow, buyerA, "79251234567")

	err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Buyers().Add(ctx, buyer.New(buyerB, "79251234567", at(0)))
	})
	if !errors.Is(err, buyer.ErrExists) {
		t.Fatalf("телефон занят: %v", err)
	}

	challenge := buyer.NewChallenge(
		buyerB,
		"79251234567",
		buyer.CallInstruction{
			ProviderRef:     "201737-542",
			CallPhone:       "78005008275",
			CallPhonePretty: "+7 (800) 500-8275",
		},
		at(0),
		5*time.Minute,
	)

	err = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.Challenges().Add(ctx, challenge); err != nil {
			return err
		}

		found, err := tx.Buyers().FindByPhone(ctx, "79251234567")
		if err != nil || found.ID != buyerA {
			t.Fatalf("покупатель по телефону: %+v %v", found, err)
		}

		loaded, err := tx.Challenges().FindByProviderRef(ctx, "201737-542")
		if err != nil {
			return err
		}
		stale := buyer.RestoreChallenge(loaded.State())

		loaded.Observe(buyer.CallConfirmed, at(1))
		if err := tx.Challenges().Update(ctx, loaded); err != nil {
			return err
		}

		stale.Observe(buyer.CallExpired, at(1))
		if err := tx.Challenges().Update(ctx, stale); !errors.Is(err, buyer.ErrChallengeConflict) {
			t.Fatalf("устаревшая проверка: %v", err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.Challenges().Get(ctx, buyerB)
		if err != nil || c.Status() != buyer.ChallengeConfirmed || c.State().Checks != 1 ||
			!c.State().LastCheckedAt.Equal(at(1)) {
			t.Fatalf("проверка после хранения: %+v %v", c.State(), err)
		}
		if _, err := tx.Challenges().Get(ctx, buyerA); !errors.Is(err, buyer.ErrChallengeNotFound) {
			t.Fatalf("нет проверки: %v", err)
		}
		if _, err := tx.Buyers().Get(ctx, buyerB); !errors.Is(err, buyer.ErrNotFound) {
			t.Fatalf("нет покупателя: %v", err)
		}

		return nil
	})
}

func TestCarts(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	seedBuyer(t, uow, buyerA, "79251234567")
	ttl := 24 * time.Hour

	c := cart.New(buyerB, buyerA, at(0), ttl)
	offer := cart.Offer{
		ID:        ids.OfferID{Tenant: "t1", Product: "p1"},
		Name:      "Чайник",
		Price:     money.FromKopecks(199000),
		Available: 5,
		Visible:   true,
	}
	_ = c.Add(offer, 2, at(0), ttl)
	_ = c.Add(
		cart.Offer{
			ID:        ids.OfferID{Tenant: "t2", Product: "p1"},
			Name:      "Кружка",
			Price:     money.FromKopecks(100),
			Available: 5,
			Visible:   true,
		},
		1,
		at(0),
		ttl,
	)

	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error { return tx.Carts().Save(ctx, c) }); err != nil {
		t.Fatal(err)
	}

	err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		loaded, err := tx.Carts().ByBuyer(ctx, buyerA)
		if err != nil {
			return err
		}

		lines := loaded.Lines()
		if len(lines) != 2 || lines[0].Offer.Product != "p1" || lines[0].Price.Kopecks() != 199000 ||
			lines[0].Quantity != 2 ||
			lines[1].Name != "Кружка" {
			t.Fatalf("строки по порядку добавления: %+v", lines)
		}

		stale := cart.Restore(loaded.State())

		loaded.Apply(
			[]cart.Check{{Offer: offer.ID, Exists: true, Price: money.FromKopecks(209000), Available: 5}},
			at(1),
			ttl,
		)
		if err := tx.Carts().Save(ctx, loaded); err != nil {
			return err
		}

		stale.Remove(offer.ID, at(1), ttl)
		if err := tx.Carts().Save(ctx, stale); !errors.Is(err, cart.ErrConflict) {
			t.Fatalf("устаревшая корзина: %v", err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		loaded, err := tx.Carts().ByBuyer(ctx, buyerA)
		if err != nil || loaded.Lines()[0].Issue != cart.IssuePriceChanged ||
			loaded.Lines()[0].Price.Kopecks() != 209000 {
			t.Fatalf("пометка после хранения: %+v %v", loaded.Lines(), err)
		}

		removed, err := tx.Carts().DeleteExpired(ctx, at(1).Add(48*time.Hour), 10)
		if err != nil || removed != 1 {
			t.Fatalf("чистка просроченных: %d %v", removed, err)
		}
		if _, err := tx.Carts().ByBuyer(ctx, buyerA); !errors.Is(err, cart.ErrNotFound) {
			t.Fatalf("корзины нет: %v", err)
		}

		return nil
	})
}

func TestCheckouts(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	seedBuyer(t, uow, buyerA, "79251234567")

	lines := []checkout.Line{
		{
			Offer:    ids.OfferID{Tenant: "t1", Product: "p1"},
			Name:     "Чайник",
			Quantity: 2,
			Price:    money.FromKopecks(199000),
		},
		{Offer: ids.OfferID{Tenant: "t2", Product: "p1"}, Name: "Кружка", Quantity: 1, Price: money.FromKopecks(35000)},
	}
	created, err := checkout.New(buyerB, buyerA, "checkout-key-0001", lines, at(0))
	if err != nil {
		t.Fatal(err)
	}

	err = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, created)
	})
	if err != nil {
		t.Fatal(err)
	}

	duplicate, _ := checkout.New("8b1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b", buyerA, "checkout-key-0001", lines, at(0))

	err = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, duplicate)
	})
	if !errors.Is(err, checkout.ErrDuplicateKey) {
		t.Fatalf("ключ занят: %v", err)
	}

	selection := checkout.Selection{
		Carrier:         "cdek",
		PickupPointCode: "MSK1",
		City:            "Город",
		Address:         "ул. Примерная, 1",
	}
	quotes := map[ids.TenantID]delivery.Quote{
		"t1": {
			Status:    delivery.QuoteAvailable,
			Cost:      money.FromKopecks(30000),
			Subsidy:   money.FromKopecks(10000),
			BuyerCost: money.FromKopecks(20000),
			MinDays:   2,
			MaxDays:   4,
			ExpiresAt: at(30),
		},
		"t2": {Status: delivery.QuoteUnavailable, Reason: "carrier_not_connected", ExpiresAt: at(30)},
	}

	err = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.Checkouts().ByKey(ctx, buyerA, "checkout-key-0001")
		if err != nil {
			return err
		}
		stale := checkout.Restore(c.State())

		if err := c.SelectDelivery(selection, quotes, at(1)); err != nil {
			return err
		}
		if err := tx.Checkouts().Update(ctx, c); err != nil {
			return err
		}

		_ = stale.SelectDelivery(selection, quotes, at(2))
		if err := tx.Checkouts().Update(ctx, stale); !errors.Is(err, checkout.ErrConflict) {
			t.Fatalf("устаревшая покупка: %v", err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.Checkouts().Get(ctx, buyerB)
		if err != nil {
			t.Fatal(err)
		}

		state := c.State()
		if *state.Delivery != selection || state.DeliveryTotal.Kopecks() != 20000 ||
			state.Total.Kopecks() != 2*199000+35000+20000 {
			t.Fatalf("покупка после хранения: %+v", state)
		}
		if q := state.Groups[0].Quote; q == nil || q.Subsidy.Kopecks() != 10000 || q.MaxDays != 4 ||
			!q.ExpiresAt.Equal(at(30)) {
			t.Fatalf("расчёт продавца: %+v", q)
		}
		if q := state.Groups[1].Quote; q == nil || q.Status != delivery.QuoteUnavailable ||
			q.Reason != "carrier_not_connected" {
			t.Fatalf("недоступный расчёт: %+v", q)
		}
		if state.Groups[1].ExternalCheckoutID != string(buyerB)+":t2" ||
			state.Groups[0].Lines[0].Price.Kopecks() != 199000 {
			t.Fatalf("группа: %+v", state.Groups[1])
		}

		return nil
	})
}

func TestPickupPointsAndLeases(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()

	point := func(code string, refreshed time.Time) delivery.PickupPoint {
		p, err := delivery.NewPickupPoint(
			"cdek",
			code,
			"Пункт "+code,
			"ул. Примерная, "+code,
			"Город",
			delivery.Location{Lat: 55.7, Lon: 37.6},
			"Пн-Пт",
			json.RawMessage(`{"code":"`+code+`"}`),
			refreshed,
		)
		if err != nil {
			t.Fatal(err)
		}

		return p
	}

	err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		return tx.PickupPoints().
			Upsert(ctx, []delivery.PickupPoint{point("A1", at(0)), point("A2", at(0)), point("A3", at(0))})
	})
	if err != nil {
		t.Fatal(err)
	}

	err = do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		updated := point("A1", at(10))
		updated.Name = "Обновлённый"
		if err := tx.PickupPoints().Upsert(ctx, []delivery.PickupPoint{updated}); err != nil {
			return err
		}

		closed, err := tx.PickupPoints().DeactivateStale(ctx, "cdek", at(10))
		if err != nil || closed != 2 {
			t.Fatalf("закрытие непришедших: %d %v", closed, err)
		}

		p, err := tx.PickupPoints().Get(ctx, "cdek", "A1")
		if err != nil || p.Name != "Обновлённый" || !p.Active || p.Location.Lat != 55.7 ||
			string(p.Payload) != `{"code": "A1"}` {
			t.Fatalf("пункт после хранения: %+v %v", p, err)
		}

		page, err := tx.PickupPoints().Page(ctx, "cdek", "A1", 10)
		if err != nil || len(page) != 2 || page[0].Code != "A2" || page[0].Active {
			t.Fatalf("страница с закрытыми: %+v %v", page, err)
		}

		if _, err := tx.PickupPoints().Get(ctx, "yandex", "A1"); !errors.Is(err, delivery.ErrPickupPointNotFound) {
			t.Fatalf("чужой перевозчик: %v", err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	leases := postgres.NewLeases(pool)
	if ok, err := leases.Acquire(ctx, "job", "pod-1", time.Minute); err != nil || !ok {
		t.Fatalf("свободная аренда: %v %v", ok, err)
	}
	if ok, _ := leases.Acquire(ctx, "job", "pod-2", time.Minute); ok {
		t.Fatal("занятая аренда")
	}
	if ok, _ := leases.Acquire(ctx, "job", "pod-1", time.Minute); !ok {
		t.Fatal("свою аренду можно продлить")
	}
	if err := leases.Release(ctx, "job", "pod-1"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := leases.Acquire(ctx, "job", "pod-2", time.Millisecond); !ok {
		t.Fatal("отпущенная аренда")
	}
	time.Sleep(5 * time.Millisecond)
	if ok, _ := leases.Acquire(ctx, "job", "pod-1", time.Minute); !ok {
		t.Fatal("истёкшая аренда")
	}
}

func TestParticipantSettingsMigrationSchedulesReindex(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	statuses, err := postgres.MigrationStatus(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	// Проверяется переход с версии 3: последующие миграции не меняют исходную точку теста.
	for _, status := range statuses {
		if status.Source.Version < 4 {
			continue
		}
		if err := postgres.MigrateDown(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}

	seed := `
		INSERT INTO tenants (tenant_id, slug, status, event_at, version, updated_at)
		VALUES ('t1', 's', 'active', now(), 1, now());

		INSERT INTO participants (tenant_id, slug, name, status, enabled, version, created_at, updated_at)
		VALUES ('t1', 's', 'n', 'onboarding', true, 1, now(), now())`
	if _, err := pool.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var (
		version int64
		shop    bool
		delayed bool
	)

	participant := `SELECT version, shop_enabled FROM participants WHERE tenant_id = 't1'`

	err = pool.QueryRow(ctx, participant).Scan(&version, &shop)
	if err != nil || version != 2 || shop {
		t.Fatalf("версия участника выросла, согласия магазина пока нет: %d %v %v", version, shop, err)
	}

	sync := `
		SELECT next_at > now() + interval '10 minutes'
		FROM outbox
		WHERE kind = 'catalog.tenant.sync' AND tenant_id = 't1'`
	if err := pool.QueryRow(ctx, sync).Scan(&delayed); err != nil || !delayed {
		t.Fatalf("переиндексация отложена до конца выката: %v %v", delayed, err)
	}
}
