//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

var w3Week2IntegrationDelivery = participant.DeliverySettings{Regions: []string{"RU-MOW"}, Carriers: []string{"cdek"}}

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

		if _, err := p.ApplyShopSettings(subsidy, money.Money{}, w3Week2IntegrationDelivery, at(1)); err != nil {
			return err
		}

		return tx.Participants().Update(ctx, p)
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, "t1")
		if err != nil || !p.DeliveryValid() || p.Subsidy() != subsidy || !p.State().SettingsSyncedAt.Equal(at(1)) {
			return fmt.Errorf("настройки магазина после хранения: %+v %w", p.State(), err)
		}

		state, err := tx.Offers().ParticipantOffers(ctx, ports.OfferKey{}, 10)
		if err != nil || len(state) != 0 {
			return fmt.Errorf("модель чтения читает новые колонки: %w", err)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
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
			return fmt.Errorf("покупатель по телефону: %+v %w", found, err)
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
			return fmt.Errorf("устаревшая проверка: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.Challenges().Get(ctx, buyerB)
		if err != nil || c.Status() != buyer.ChallengeConfirmed || c.State().Checks != 1 ||
			!c.State().LastCheckedAt.Equal(at(1)) {
			return fmt.Errorf("проверка после хранения: %+v %w", c.State(), err)
		}
		if _, err := tx.Challenges().Get(ctx, buyerA); !errors.Is(err, buyer.ErrChallengeNotFound) {
			return fmt.Errorf("нет проверки: %w", err)
		}
		if _, err := tx.Buyers().Get(ctx, buyerB); !errors.Is(err, buyer.ErrNotFound) {
			return fmt.Errorf("нет покупателя: %w", err)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
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
			return fmt.Errorf("строки по порядку добавления: %+v", lines)
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
			return fmt.Errorf("устаревшая корзина: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		loaded, err := tx.Carts().ByBuyer(ctx, buyerA)
		if err != nil || loaded.Lines()[0].Issue != cart.IssuePriceChanged ||
			loaded.Lines()[0].Price.Kopecks() != 209000 {
			return fmt.Errorf("пометка после хранения: %+v %w", loaded.Lines(), err)
		}

		removed, err := tx.Carts().DeleteExpired(ctx, at(1).Add(48*time.Hour), 10)
		if err != nil || removed != 1 {
			return fmt.Errorf("чистка просроченных: %d %w", removed, err)
		}
		if _, err := tx.Carts().ByBuyer(ctx, buyerA); !errors.Is(err, cart.ErrNotFound) {
			return fmt.Errorf("корзины нет: %w", err)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
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
			return fmt.Errorf("устаревшая покупка: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.Checkouts().Get(ctx, buyerB)
		if err != nil {
			return fmt.Errorf("%w", err)
		}

		state := c.State()
		if *state.Delivery != selection || state.DeliveryTotal.Kopecks() != 20000 ||
			state.Total.Kopecks() != 2*199000+35000+20000 {
			return fmt.Errorf("покупка после хранения: %+v", state)
		}
		if q := state.Groups[0].Quote; q == nil || q.Subsidy.Kopecks() != 10000 || q.MaxDays != 4 ||
			!q.ExpiresAt.Equal(at(30)) {
			return fmt.Errorf("расчёт продавца: %+v", q)
		}
		if q := state.Groups[1].Quote; q == nil || q.Status != delivery.QuoteUnavailable ||
			q.Reason != "carrier_not_connected" {
			return fmt.Errorf("недоступный расчёт: %+v", q)
		}
		if state.Groups[1].ExternalCheckoutID != string(buyerB)+":t2" ||
			state.Groups[0].Lines[0].Price.Kopecks() != 199000 {
			return fmt.Errorf("группа: %+v", state.Groups[1])
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
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
			return fmt.Errorf("закрытие непришедших: %d %w", closed, err)
		}

		p, err := tx.PickupPoints().Get(ctx, "cdek", "A1")
		if err != nil || p.Name != "Обновлённый" || !p.Active || p.Location.Lat != 55.7 ||
			string(p.Payload) != `{"code": "A1"}` {
			return fmt.Errorf("пункт после хранения: %+v %w", p, err)
		}

		page, err := tx.PickupPoints().Page(ctx, "cdek", "A1", 10)
		if err != nil || len(page) != 2 || page[0].Code != "A2" || page[0].Active {
			return fmt.Errorf("страница с закрытыми: %+v %w", page, err)
		}

		if _, err := tx.PickupPoints().Get(ctx, "yandex", "A1"); !errors.Is(err, delivery.ErrPickupPointNotFound) {
			return fmt.Errorf("чужой перевозчик: %w", err)
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

	if err := postgres.MigrateDownTo(ctx, pool, 3); err != nil {
		t.Fatal(err)
	}

	// Миграции 4 и 21 обе инвалидируют старую видимость.
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

	participant := `SELECT version, delivery_valid FROM participants WHERE tenant_id = 't1'`

	err := pool.QueryRow(ctx, participant).Scan(&version, &shop)
	// Версия 1 увеличивается до 2 при добавлении согласия магазина (4),
	// затем до 3 при введении доставки (21); миграция 37 данные не переносит.
	if err != nil || version != 3 || shop {
		t.Fatalf("две инвалидации видимости, доставка ещё не настроена: %d %v %v", version, shop, err)
	}

	sync := `
		SELECT next_at > now() + interval '10 minutes'
		FROM outbox
		WHERE kind = 'catalog.tenant.sync' AND tenant_id = 't1'`
	if err := pool.QueryRow(ctx, sync).Scan(&delayed); err != nil || !delayed {
		t.Fatalf("переиндексация отложена до конца выката: %v %v", delayed, err)
	}
}
