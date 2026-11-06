//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

var w3RepositoriesIntegrationDelivery = participant.DeliverySettings{
	Regions:  []string{"RU-MOW"},
	Carriers: []string{"cdek"},
}
var w3RepositoriesIntegrationSubsidy = participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed}

func TestMigrationsDownAndUp(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	statuses, err := postgres.MigrationStatus(ctx, pool)
	if err != nil || len(statuses) == 0 {
		t.Fatalf("статус миграций: %d, %v", len(statuses), err)
	}

	for range statuses {
		if err := postgres.MigrateDown(ctx, pool); err != nil {
			t.Fatalf("откат: %v", err)
		}
	}

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("повторное применение: %v", err)
	}
}

func registryEvent(
	id ids.TenantID,
	slug string,
	status participant.TenantStatus,
	minute int,
) participant.RegistryEvent {
	return participant.RegistryEvent{
		TenantID: id,
		Slug:     slug,
		Status:   status,
		Cell:     "cell-a",
		Plan:     json.RawMessage(`{"code":"base","limits":{"products":100}}`),
		Hosts:    json.RawMessage(`[{"host":"a.example"}]`),
		At:       at(minute),
	}
}

func seedTenant(t *testing.T, uow *postgres.UnitOfWork, id ids.TenantID, slug string, status participant.TenantStatus) {
	t.Helper()
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		tenant, err := participant.NewTenant(registryEvent(id, slug, status, 0), at(0))
		if err != nil {
			return err
		}

		return tx.Registry().Save(ctx, tenant)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegistryRoundTripAndConflict(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	ctx := context.Background()
	seedTenant(t, uow, "t1", "shop-one", participant.TenantCreating)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		reformatted := registryEvent("t1", "shop-one", participant.TenantCreating, 0)
		reformatted.Plan = json.RawMessage(`{"limits":{"products":100},"code":"base"}`)
		if changed, _ := tenant.Apply(reformatted, at(1)); changed {
			return errors.New("тот же снимок после хранения в jsonb не считается изменением")
		}
		stale := participant.RestoreTenant(tenant.State())
		if _, err := tenant.Apply(registryEvent("t1", "shop-one", participant.TenantActive, 1), at(1)); err != nil {
			return err
		}
		if err := tx.Registry().Save(ctx, tenant); err != nil {
			return err
		}

		if _, err := stale.Apply(registryEvent("t1", "shop-one", participant.TenantSuspended, 2), at(2)); err != nil {
			return err
		}
		if err := tx.Registry().Save(ctx, stale); !errors.Is(err, participant.ErrTenantConflict) {
			return fmt.Errorf("устаревшая версия: ожидалось ErrTenantConflict, получено %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil || tenant.Status() != participant.TenantActive || tenant.Version() != 2 {
			return fmt.Errorf("запись реестра: %+v, %w", tenant, err)
		}
		if _, err := tx.Registry().Get(ctx, "missing"); !errors.Is(err, participant.ErrTenantNotFound) {
			return fmt.Errorf("ожидалось ErrTenantNotFound, получено %w", err)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestParticipantsRepository(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	ctx := context.Background()
	seedTenant(t, uow, "t1", "shop-one", participant.TenantActive)
	seedTenant(t, uow, "t2", "shop-one", participant.TenantActive)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, _ := tx.Registry().Get(ctx, "t1")
		p, err := participant.New(tenant, "Магазин", at(1))
		if err != nil {
			return err
		}

		return tx.Participants().Add(ctx, p)
	})
	if err != nil {
		t.Fatal(err)
	}

	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, _ := tx.Registry().Get(ctx, "t1")
		p, _ := participant.New(tenant, "Дубль", at(1))

		return tx.Participants().Add(ctx, p)
	})
	if !errors.Is(err, participant.ErrAlreadyExists) {
		t.Fatalf("ожидалось ErrAlreadyExists, получено %v", err)
	}

	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, _ := tx.Registry().Get(ctx, "t2")
		p, _ := participant.New(tenant, "Тот же слаг", at(1))

		return tx.Participants().Add(ctx, p)
	})
	if !errors.Is(err, participant.ErrSlugTaken) {
		t.Fatalf("ожидалось ErrSlugTaken, получено %v", err)
	}

	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		ghost, _ := participant.NewTenant(registryEvent("ghost", "ghost", participant.TenantActive, 0), at(0))
		p, _ := participant.New(ghost, "Призрак", at(1))

		return tx.Participants().Add(ctx, p)
	})
	if !errors.Is(err, participant.ErrTenantUnknown) {
		t.Fatalf("ожидалось ErrTenantUnknown, получено %v", err)
	}

	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, "t1")
		if err != nil {
			return err
		}
		stale := participant.Restore(p.State())

		if _, err := p.ApplyShopSettings(
			w3RepositoriesIntegrationSubsidy,
			money.Money{},
			w3RepositoriesIntegrationDelivery,
			at(2),
		); err != nil {
			return err
		}
		if err := tx.Participants().Update(ctx, p); err != nil {
			return err
		}

		stale.Enable(at(3))
		if err := tx.Participants().Update(ctx, stale); !errors.Is(err, participant.ErrConflict) {
			return fmt.Errorf("переход по устаревшему статусу и версии: ожидалось ErrConflict, получено %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, "t1")
		if err != nil {
			return err
		}
		if p.Stage() != participant.StageCandidate || p.Version() != 2 || p.Enabled() {
			return fmt.Errorf("участник: %+v", p.State())
		}

		items, total, err := tx.Participants().List(ctx, participant.Page{Limit: 10})
		if err != nil || total != 1 || len(items) != 1 {
			return fmt.Errorf("список: %d %d %w", total, len(items), err)
		}

		if _, err := tx.Participants().Get(ctx, "t2"); !errors.Is(err, participant.ErrNotFound) {
			return fmt.Errorf("ожидалось ErrNotFound, получено %w", err)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func snapshot(tenant ids.TenantID, product ids.ProductID, revision int64, minute int) catalog.Snapshot {
	promo := money.FromKopecks(8000)

	return catalog.Snapshot{
		TenantID:  tenant,
		ProductID: product,
		Revision:  revision,
		At:        at(minute),
		Card: catalog.Card{
			Name:        "Товар " + string(product),
			Description: "Описание",
			Image:       "/img.png",
			URL:         "/p",
			SKU:         "SKU-1",
			Categories:  []catalog.Category{{ID: "c1", Name: "Категория", URL: "/c"}},
			VariantsID:  "v1",
			Visible:     true,
		},
		Price:      money.FromKopecks(10000),
		PromoPrice: &promo,
		Available:  5,
		PriceProfiles: []catalog.PriceProfile{
			{
				Key:            catalog.DefaultPriceProfile,
				Price:          money.FromKopecks(10000),
				EffectivePrice: money.FromKopecks(10000),
			},
		},
	}
}

func TestProductInsertConflict(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	ctx := context.Background()
	save := func() error {
		return uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			product, err := catalog.NewProduct(snapshot("t1", "p1", 1, 0), at(0))
			if err != nil {
				return err
			}

			return tx.Products().Save(ctx, product)
		})
	}

	if err := save(); err != nil {
		t.Fatal(err)
	}
	if err := save(); !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("второе заведение того же товара: ожидалось ErrConflict, получено %v", err)
	}
}

func TestSwallowedQueryErrorIsNotUnavailable(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	ctx := context.Background()
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		_, _ = tx.Products().Get(ctx, "t1", "p1")
		duplicate, _ := catalog.NewProduct(snapshot("t1", "p1", 1, 0), at(0))
		_ = tx.Products().Save(ctx, duplicate)
		again, _ := catalog.NewProduct(snapshot("t1", "p1", 1, 0), at(0))
		_ = tx.Products().Save(ctx, again)

		return nil
	})
	if err == nil || errors.Is(err, postgres.ErrUnavailable) {
		t.Fatalf("прерванная базой транзакция — не недоступность хранилища: %v", err)
	}
}

func TestProductRoundTrip(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	ctx := context.Background()
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		product, _ := catalog.NewProduct(snapshot("t1", "p1", 1, 0), at(0))

		return tx.Products().Save(ctx, product)
	})
	if err != nil {
		t.Fatal(err)
	}

	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		product, err := tx.Products().Get(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		state := product.State()
		if state.Card.Name != "Товар p1" ||
			state.Card.Categories[0].Name != "Категория" ||
			state.Price.Kopecks() != 10000 ||
			state.PromoPrice == nil ||
			state.PromoPrice.Kopecks() != 8000 ||
			state.Available != 5 ||
			!state.ProjectedAt.Equal(at(0)) {
			return fmt.Errorf("товар после хранения: %+v", state)
		}
		stale := catalog.RestoreProduct(state)
		deletion := catalog.Snapshot{TenantID: "t1", ProductID: "p1", Revision: 2, Deleted: true, At: at(1)}
		if _, err := product.Apply(deletion, at(1)); err != nil {
			return err
		}
		if err := tx.Products().Save(ctx, product); err != nil {
			return err
		}

		if _, err := stale.Apply(snapshot("t1", "p1", 3, 2), at(2)); err != nil {
			return err
		}
		if err := tx.Products().Save(ctx, stale); !errors.Is(err, catalog.ErrConflict) {
			return fmt.Errorf("устаревшая версия: ожидалось ErrConflict, получено %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		product, err := tx.Products().Get(ctx, "t1", "p1")
		if err != nil || !product.State().Deleted || product.State().PromoPrice != nil || product.Version() != 2 {
			return fmt.Errorf("удалённый товар: %+v, %w", product.State(), err)
		}
		if _, err := tx.Products().Get(ctx, "t1", "missing"); !errors.Is(err, catalog.ErrProductNotFound) {
			return fmt.Errorf("ожидалось ErrProductNotFound, получено %w", err)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSignals(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	ctx := context.Background()
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		signals := catalog.NewSignals("t1", "p1")
		if _, err := signals.ApplyStock(catalog.StockSignal{Available: 2, At: at(1)}, at(1)); err != nil {
			return err
		}
		if err := tx.Signals().Save(ctx, signals); err != nil {
			return err
		}
		price := catalog.PriceSignal{Price: money.FromKopecks(700), At: at(2)}
		if _, err := signals.ApplyPrice(price, at(2)); err != nil {
			return err
		}
		if err := tx.Signals().Save(ctx, signals); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		signals, err := tx.Signals().Get(ctx, "t1", "p1")
		state := signals.State()
		if err != nil || !state.HasStock || state.Available != 2 || !state.HasPrice || state.Price.Kopecks() != 700 ||
			state.Version != 2 {
			return fmt.Errorf("сигналы: %+v, %w", state, err)
		}
		if _, err := tx.Signals().Get(ctx, "t1", "p2"); !errors.Is(err, catalog.ErrSignalsNotFound) {
			return fmt.Errorf("ожидалось ErrSignalsNotFound, получено %w", err)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUnitOfWorkRollsBack(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	ctx := context.Background()
	failure := errors.New("сбой сценария")
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		product, _ := catalog.NewProduct(snapshot("t1", "p1", 1, 0), at(0))
		if err := tx.Products().Save(ctx, product); err != nil {
			return err
		}

		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("ошибка сценария возвращается как есть: %v", err)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("паника пробрасывается дальше")
			}
		}()

		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			product, _ := catalog.NewProduct(snapshot("t1", "p2", 1, 0), at(0))
			_ = tx.Products().Save(ctx, product)

			panic("сбой")
		}); err != nil {
			t.Fatal(err)
		}
	}()

	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for _, id := range []ids.ProductID{"p1", "p2"} {
			if _, err := tx.Products().Get(ctx, "t1", id); !errors.Is(err, catalog.ErrProductNotFound) {
				return fmt.Errorf("%s: откат не оставляет записей, получено %w", id, err)
			}
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
