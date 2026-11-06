//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	app "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/platformapi"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestStoreCardRepositoryRoundTripAndConflict(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	ctx := context.Background()
	seedTenant(t, uow, "t1", "shop", participant.TenantActive)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		p, err := participant.New(tenant, "Магазин", at(0))
		if err != nil {
			return err
		}
		if err := tx.Participants().Add(ctx, p); err != nil {
			return err
		}
		if _, err := p.ApplyShopSettings(
			w3RepositoriesIntegrationSubsidy,
			money.Money{},
			w3RepositoriesIntegrationDelivery,
			at(1),
		); err != nil {
			return err
		}
		profile := participant.Profile{Name: "Новое имя", Slogan: "Слоган"}
		ready := participant.CardReadiness{Ready: true}
		if _, err := p.ApplyStoreCard(profile, ready, &participant.Returns{Days: 14}, at(1)); err != nil {
			return err
		}
		if _, err := p.HideCard("slogan", "Причина", at(1)); err != nil {
			return err
		}
		if err := tx.Participants().Update(ctx, p); err != nil {
			return err
		}
		loaded, err := tx.Participants().Get(ctx, "t1")
		if err != nil {
			return err
		}
		card := loaded.State().StoreCard
		if !loaded.CardReady() || loaded.Name() != "Новое имя" || len(card.Hidden) != 1 || card.Returns == nil {
			return fmt.Errorf("карточка не сохранилась: %+v", card)
		}
		count, err := tx.Participants().Count(ctx, participant.Page{Queue: true})
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("очередь: %d", count)
		}
		stale, err := tx.Participants().Get(ctx, "t1")
		if err != nil {
			return err
		}
		profile.Slogan = "Исправлен"
		if _, err := loaded.ApplyStoreCard(profile, ready, card.Returns, at(2)); err != nil {
			return err
		}
		if err := tx.Participants().Update(ctx, loaded); err != nil {
			return err
		}
		if _, err := stale.HideCard("logo", "Причина", at(3)); err != nil {
			return err
		}
		if err := tx.Participants().Update(ctx, stale); !errors.Is(err, participant.ErrConflict) {
			return fmt.Errorf("ожидался конфликт: %w", err)
		}
		loaded, err = tx.Participants().Get(ctx, "t1")
		if err != nil {
			return err
		}
		if len(loaded.State().StoreCard.Hidden) != 0 {
			return fmt.Errorf("скрытие не снято")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStoreCardOfferProjection(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	ctx := context.Background()
	seedTenant(t, uow, "t1", "shop", participant.TenantActive)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		p, err := participant.New(tenant, "Магазин", at(0))
		if err != nil {
			return err
		}
		_, err = p.ApplyStoreCard(participant.Profile{}, participant.CardReadiness{Ready: true}, nil, at(0))
		if err != nil {
			return err
		}
		if _, err := p.ApplyShopSettings(
			w3RepositoriesIntegrationSubsidy,
			money.Money{},
			w3RepositoriesIntegrationDelivery,
			at(0),
		); err != nil {
			return err
		}
		if _, err := p.Enable(at(0)); err != nil {
			return err
		}
		if err := tx.Participants().Add(ctx, p); err != nil {
			return err
		}
		snap := snapshot("t1", "p1", 1, 0)
		snap.Card.Publication.Published = true
		snap.Card.Image = "image"
		product, err := catalog.NewProduct(snap, at(0))
		if err != nil {
			return err
		}
		if err := tx.Products().Save(ctx, product); err != nil {
			return err
		}
		verdict := moderation.NewVerdict("t1", "p1")
		verdict.Evaluate(1, 0, moderation.Rules{}, moderation.Input{}, at(0))
		return tx.ModerationVerdicts().Save(ctx, verdict)
	})
	if err != nil {
		t.Fatal(err)
	}
	service := app.NewService(uow, fake.NewClock(at(1)), &fake.IDs{})
	if err := service.RefreshStoreCard(ctx, ports.Task{Kind: ports.TaskStoreCard, TenantID: "t1"}); err != nil {
		t.Fatal(err)
	}
	summary, err := service.StoreSummary(ctx, "t1")
	if err != nil || summary.Offers != 1 || summary.FromPrice == nil || len(summary.Showcase) != 1 {
		t.Fatal(summary, err)
	}
	page, err := service.Shops(ctx, "", 20, 0)
	if err != nil || page.Total != 1 {
		t.Fatal(page, err)
	}
	header, err := service.Shop(ctx, "shop")
	if err != nil || header.TenantID != "t1" {
		t.Fatal(header, err)
	}
}

// Серверный JSON проходит реальный адаптер и jsonb, который меняет порядок ключей.
func TestSettingsServerJSONStableVersion(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	seedTenant(t, uow, "t1", "shop", participant.TenantActive)
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		p, err := participant.New(tenant, "Прежнее имя", at(0))
		if err != nil {
			return err
		}
		return tx.Participants().Add(ctx, p)
	}); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../platformapi/testdata/settings_marketplace.json")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(fixture); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client := platformapi.New(platformapi.Config{BaseURL: server.URL, Token: "test"})
	clock := fake.NewClock(at(1))
	syncer := app.NewSettings(uow, client, clock, &fake.IDs{})
	if changed, err := syncer.SyncOne(ctx, "t1"); err != nil || !changed {
		t.Fatal(changed, err)
	}
	var version int64
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, "t1")
		if err != nil {
			return err
		}
		version = p.Version()
		if p.State().StoreCard.Returns == nil {
			t.Fatal(p.State())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if changed, err := syncer.SyncOne(ctx, "t1"); err != nil || changed {
		t.Fatal(changed, err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, "t1")
		if err != nil {
			return err
		}
		if p.Version() != version {
			t.Fatalf("версия %d -> %d", version, p.Version())
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreCardCandidateProjectionAndSalesRevision(t *testing.T) {
	uow := postgres.NewUnitOfWork(pgtest.Pool(t))
	ctx := context.Background()
	seedTenant(t, uow, "t1", "shop", participant.TenantActive)
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		p, err := participant.New(tenant, "Магазин", at(0))
		if err != nil {
			return err
		}
		if err := tx.Participants().Add(ctx, p); err != nil {
			return err
		}
		for _, id := range []ids.ProductID{"valid", "invalid"} {
			snap := snapshot("t1", id, 1, 0)
			snap.Card.Marketplace = &taxonomy.Card{Published: true, CategoryID: "leaf"}
			snap.Card.Publication = taxonomy.Publication{Published: id == "valid", CategoryID: "leaf"}
			snap.SalesCount = 7
			product, err := catalog.NewProduct(snap, at(0))
			if err != nil {
				return err
			}
			if err := tx.Products().Save(ctx, product); err != nil {
				return err
			}
			loaded, err := tx.Products().Get(ctx, "t1", id)
			if err != nil {
				return err
			}
			if loaded.State().SalesCount != 7 {
				t.Fatal(loaded.State())
			}
			snap.SalesCount = 99
			if changed, err := loaded.Apply(snap, at(1)); err != nil || changed {
				t.Fatal(changed, err)
			}
			if err := tx.Products().Save(ctx, loaded); err != nil {
				return err
			}
		}
		states, err := tx.Offers().StoreCardOffers(ctx, "t1", "", 10)
		if err != nil {
			return err
		}
		if len(states) != 2 || states[0].Product.State().Card.Publication.Published {
			t.Fatal(states)
		}
		items, total, err := tx.Participants().List(ctx, participant.Page{Queue: true, Limit: 10})
		if err != nil {
			return err
		}
		if total != 0 || len(items) != 0 {
			t.Fatal(items, total)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service := app.NewService(uow, fake.NewClock(at(1)), &fake.IDs{})
	if err := service.RefreshStoreCard(ctx, ports.Task{Kind: ports.TaskStoreCard, TenantID: "t1"}); err != nil {
		t.Fatal(err)
	}
	summary, err := service.StoreSummary(ctx, "t1")
	if err != nil || summary.Offers != 1 || len(summary.Showcase) != 1 || summary.Showcase[0].ProductID != "valid" {
		t.Fatal(summary, err)
	}
	page, err := service.Shops(ctx, "", 20, 0)
	if err != nil || page.Total != 0 {
		t.Fatal(page, err)
	}
}

func TestStoredShopsCategoryOrderAndPagination(t *testing.T) {
	uow := postgres.NewUnitOfWork(pgtest.Pool(t))
	ctx := context.Background()
	for i, tenantID := range []ids.TenantID{"t1", "t2", "t3"} {
		slug := string(tenantID)
		seedTenant(t, uow, tenantID, slug, participant.TenantActive)
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			tenant, err := tx.Registry().Get(ctx, tenantID)
			if err != nil {
				return err
			}
			p, err := participant.New(tenant, "Магазин", at(0))
			if err != nil {
				return err
			}
			if err := tx.Participants().Add(ctx, p); err != nil {
				return err
			}
			counts := []int{3, 5, 9}
			categoryCounts := []int{2, 1, 9}
			summary := ports.StoreSummary{
				Offers:     counts[i],
				Categories: []ports.StoreCategory{{ID: "root", Name: "Категория", Count: counts[i]}},
				CategoryCounts: map[string]ports.StoreCategory{
					"root": {ID: "root", Name: "Категория", Count: counts[i]},
					"leaf": {ID: "leaf", Name: "Подкатегория", Count: categoryCounts[i]},
				},
				Showcase: []ports.StoreProduct{},
				Hidden:   []participant.CardHidden{},
			}
			return tx.StoreCards().Save(ctx, ports.StoreRecord{
				Summary:    summary,
				Header:     ports.ShopHeader{TenantID: tenantID, Slug: slug, Name: "Магазин", Offers: counts[i]},
				Card:       ports.ShopCard{TenantID: tenantID, Slug: slug, Name: "Магазин", Offers: counts[i]},
				Public:     i != 2,
				PagePublic: i != 2,
			})
		}); err != nil {
			t.Fatal(err)
		}
	}
	service := app.NewService(uow, fake.NewClock(at(1)), &fake.IDs{})
	for _, category := range []string{"", "leaf"} {
		page, err := service.Shops(ctx, category, 1, 0)
		if err != nil || page.Total != 2 || len(page.Items) != 1 {
			t.Fatal(page, err)
		}
		want := ids.TenantID("t2")
		if category != "" {
			want = "t1"
			if page.Items[0].Category.ID != "leaf" || page.Items[0].Offers != 2 {
				t.Fatal(page)
			}
		}
		if page.Items[0].TenantID != want {
			t.Fatal(page)
		}
		page, err = service.Shops(ctx, category, 1, 2)
		if err != nil || page.Total != 2 || len(page.Items) != 0 {
			t.Fatal(page, err)
		}
	}
	header, err := service.Shop(ctx, "t1")
	if err != nil || header.TenantID != "t1" {
		t.Fatal(header, err)
	}
	if _, err := service.Shop(ctx, "t3"); !errors.Is(err, participant.ErrNotFound) {
		t.Fatal(err)
	}
	summary, err := service.StoreSummary(ctx, "t3")
	if err != nil || summary.Offers != 9 {
		t.Fatal(summary, err)
	}
}
