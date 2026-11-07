package taxonomy_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestRevalidationRetriesProductConflict(t *testing.T) {
	store := fake.NewStore()
	seedValues(t, store, "v1")
	uow := &fake.ProductConflict{UnitOfWork: store, Offer: ids.OfferID{Tenant: "t1", Product: "p2"}}
	service := apptaxonomy.NewService(uow, fake.NewClock(time.Now()), nil, 0)
	for _, id := range []ids.ProductID{"p1", "p2", "p3"} {
		store.SeedProduct(catalog.ProductState{TenantID: "t1", ProductID: id, Version: 1})
	}
	if err := applyDefinition(context.Background(), service, schema(1)); err != nil {
		t.Fatal(err)
	}
	runRevalidation(t, service, 1)
	for _, id := range []ids.ProductID{"p1", "p2", "p3"} {
		p, _ := store.Product("t1", id)
		if p.Card.Publication.Version != 1 {
			t.Fatalf("пропущен %s", id)
		}
		if id == "p2" && (p.Version != 3 || p.Card.ModelKey != "parallel-writer") {
			t.Fatalf("потерян параллельный писатель: %+v", p)
		}
	}
	if uow.Conflicts != 1 || !readProgress(t, store).Done {
		t.Fatal("пачка не завершилась")
	}
}

func TestStoreCardsRefreshWithoutPublicationChange(t *testing.T) {
	for _, change := range []string{"root", "leaf-with-title"} {
		t.Run(change, func(t *testing.T) {
			store := fake.NewStore()
			seedValues(t, store, "v1")
			service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil, 0)
			d := schema(1)
			d.Categories[0].Title = "Полное имя"
			d.Categories = []taxonomy.Category{
				{ID: "root", Name: "Корень", Slug: "root", Children: d.Categories},
			}
			if err := applyDefinition(context.Background(), service, d); err != nil {
				t.Fatal(err)
			}
			runRevalidation(t, service, 1)
			card := &taxonomy.Card{CategoryID: "c1", Published: true}
			for _, tenant := range []ids.TenantID{"t1", "t2"} {
				store.SeedProduct(catalog.ProductState{
					TenantID:  tenant,
					ProductID: "p1",
					Version:   1,
					Card:      catalog.Card{Marketplace: card, Publication: d.Check(card)},
				})
			}
			if change == "root" {
				d.Categories[0].Name = "Новое имя"
			} else {
				d.Categories[0].Children[0].Name = "Новое имя"
			}
			if _, err := service.LoadPart(context.Background(), "categories", d, 1); err != nil {
				t.Fatal(err)
			}
			runRevalidation(t, service, 2)
			tenants := map[ids.TenantID]bool{}
			for _, row := range store.OutboxRows() {
				if row.Kind == ports.TaskStoreCard {
					tenants[row.TenantID] = true
				}
			}
			if len(tenants) != 2 {
				t.Fatal("не обновлены сводки обоих магазинов")
			}
			for tenant := range tenants {
				p, _ := store.Product(tenant, "p1")
				if p.Version != 1 {
					t.Fatal("неизменная публикация сохранена")
				}
			}
		})
	}
}

func TestPolicyRollbackUsesVerdictSnapshot(t *testing.T) {
	store := fake.NewStore()
	seedValues(t, store, "v1")
	clock := fake.NewClock(time.Now())
	evaluator := appmoderation.NewService(store, clock, nil, appmoderation.Config{})
	service := apptaxonomy.NewService(store, clock, evaluator, 0)
	d := schema(1)
	d.Categories[0].Moderation = "auto"
	ctx := context.Background()
	if err := applyDefinition(ctx, service, d); err != nil {
		t.Fatal(err)
	}
	card := &taxonomy.Card{CategoryID: "c1", Published: true}
	store.SeedProduct(catalog.ProductState{
		TenantID:  "t1",
		ProductID: "p1",
		Version:   1,
		Revision:  1,
		Card:      catalog.Card{Name: "Товар", Marketplace: card, Publication: d.Check(card)},
	})
	store.SeedParticipant(participant.State{TenantID: "t1", Trust: participant.Trusted})
	if err := evaluator.Reevaluate(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	d.Categories[0].Moderation = "forbidden"
	if _, err := service.LoadPart(ctx, "categories", d, 1); err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Reevaluate(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	assertVerdict(t, store, moderation.Rejected, 2)
	p, _ := store.Product("t1", "p1")
	if p.Card.Publication.Version != 1 {
		t.Fatal("переоценка изменила публикацию")
	}
	d.Categories[0].Moderation = "auto"
	if _, err := service.LoadPart(ctx, "categories", d, 2); err != nil {
		t.Fatal(err)
	}
	runRevalidation(t, service, 3)
	assertVerdict(t, store, moderation.Allowed, 3)
}

func assertVerdict(t *testing.T, store *fake.Store, status moderation.Status, version int64) {
	t.Helper()
	if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		verdict, err := tx.ModerationVerdicts().Get(ctx, "t1", "p1")
		if err == nil && (verdict.State().Status != status || verdict.State().TaxonomyVersion != version) {
			t.Fatalf("неверный вердикт: %+v", verdict.State())
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRevalidationRetriesVerdictConflict(t *testing.T) {
	store := fake.NewStore()
	seedValues(t, store, "v1")
	evaluator := &fake.TaxonomyEvaluator{
		FailOffer: ids.OfferID{Tenant: "t1", Product: "p2"},
		Err:       moderation.ErrConflict,
		FailOnce:  true,
	}
	service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), evaluator, 0)
	for _, id := range []ids.ProductID{"p1", "p2", "p3"} {
		store.SeedProduct(catalog.ProductState{TenantID: "t1", ProductID: id, Version: 1})
	}
	if err := applyDefinition(context.Background(), service, schema(1)); err != nil {
		t.Fatal(err)
	}
	runRevalidation(t, service, 1)
	if len(evaluator.Calls) != 4 || !readProgress(t, store).Done {
		t.Fatal("конфликт откатил пачку")
	}
	for _, id := range []ids.ProductID{"p1", "p2", "p3"} {
		p, _ := store.Product("t1", id)
		if p.Version != 2 || p.Card.Publication.Version != 1 {
			t.Fatalf("повторное сохранение: %+v", p)
		}
	}
}

func TestOperatorDecisionDoesNotRepeatEvaluation(t *testing.T) {
	for _, change := range []string{"policy", "published"} {
		t.Run(change, func(t *testing.T) {
			store := fake.NewStore()
			seedValues(t, store, "v1")
			evaluator := &fake.TaxonomyEvaluator{}
			service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), evaluator, 0)
			d := schema(1)
			if err := applyDefinition(context.Background(), service, d); err != nil {
				t.Fatal(err)
			}
			runRevalidation(t, service, 1)
			card := &taxonomy.Card{CategoryID: "c1", Published: true}
			store.SeedProduct(catalog.ProductState{
				TenantID:  "t1",
				ProductID: "p1",
				Revision:  7,
				Version:   1,
				Card:      catalog.Card{Marketplace: card, Publication: d.Check(card)},
			})
			if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
				v := moderation.NewVerdict("t1", "p1")
				v.Evaluate(7, 1, moderation.Rules{}, moderation.Input{Policy: "auto"}, time.Now())
				v.Decide(moderation.Allow, "", time.Now())
				return tx.ModerationVerdicts().Save(ctx, v)
			}); err != nil {
				t.Fatal(err)
			}

			if change == "published" {
				d.Schemas["s1"] = taxonomy.Schema{Attributes: []string{"a1"}}
				if _, err := service.LoadPart(context.Background(), "schemas", d, 1); err != nil {
					t.Fatal(err)
				}
				runRevalidation(t, service, 2)
				p, _ := store.Product("t1", "p1")
				if p.Version != 2 || p.Card.Publication.Published || p.Card.Publication.Version != 2 {
					t.Fatal("изменённая публикация не сохранена")
				}
				found := false
				for _, row := range store.OutboxRows() {
					found = found || row.Kind == moderationstatus.TaskOffer
				}
				if !found || len(evaluator.Calls) != 0 {
					t.Fatalf("задача предложения=%v, вызовов evaluator=%d", found, len(evaluator.Calls))
				}
				assertVerdict(t, store, moderation.Allowed, 1)
				return
			}
			d.Categories[0].Moderation = "forbidden"
			for version := int64(2); version <= 4; version++ {
				if _, err := service.LoadPart(context.Background(), "categories", d, version-1); err != nil {
					t.Fatal(err)
				}
				runRevalidation(t, service, version)
			}
			p, _ := store.Product("t1", "p1")
			if len(evaluator.Calls) != 0 || p.Version != 1 || len(store.OutboxRows()) != 1 {
				t.Fatal("решение оператора вызвало повторную работу")
			}
			assertVerdict(t, store, moderation.Allowed, 1)
		})
	}
}

func TestCardsVersionSurvivesSupersessionAndStopsRefresh(t *testing.T) {
	store := fake.NewStore()
	seedValues(t, store, "v1")
	clock := fake.NewClock(time.Now())
	delay := 37 * time.Second
	service := apptaxonomy.NewService(store, clock, nil, delay)
	d := schema(1)
	d.Categories = []taxonomy.Category{{ID: "root", Name: "Корень", Slug: "root", Children: d.Categories}}
	if err := applyDefinition(context.Background(), service, d); err != nil {
		t.Fatal(err)
	}
	runRevalidation(t, service, 1)
	card := &taxonomy.Card{CategoryID: "c1", Published: true}
	for i := 0; i < 201; i++ {
		store.SeedProduct(catalog.ProductState{
			TenantID:  ids.TenantID(fmt.Sprintf("t%03d", i)),
			ProductID: "p1",
			Version:   1,
			Card:      catalog.Card{Marketplace: card, Publication: d.Check(card)},
		})
	}
	d.Categories[0].Name = "Новое имя"
	if _, err := service.LoadPart(context.Background(), "categories", d, 1); err != nil {
		t.Fatal(err)
	}
	runRevalidation(t, service, 2)
	if readProgress(t, store).CardsVersion != 1 {
		t.Fatal("версия сводок продвинута до конца прохода")
	}
	runRevalidation(t, service, 2)
	if readProgress(t, store).CardsVersion != 2 {
		t.Fatal("версия сводок не сохранена")
	}
	completeCardTasks(t, store, clock.Now(), delay, 201)
	d.Categories[0].Name = "Корень"
	if _, err := service.LoadPart(context.Background(), "categories", d, 2); err != nil {
		t.Fatal(err)
	}
	runRevalidation(t, service, 3)
	if _, err := service.LoadPart(context.Background(), "attributes", d, 3); err != nil {
		t.Fatal(err)
	}
	if readProgress(t, store).CardsVersion != 2 {
		t.Fatal("замещение потеряло версию сводок")
	}
	runRevalidation(t, service, 4)
	runRevalidation(t, service, 4)
	completeCardTasks(t, store, clock.Now(), delay, 201)
	for version := int64(5); version <= 7; version++ {
		if _, err := service.LoadPart(context.Background(), "attributes", d, version-1); err != nil {
			t.Fatal(err)
		}
		runRevalidation(t, service, version)
		runRevalidation(t, service, version)
		completeCardTasks(t, store, clock.Now(), delay, 0)
	}
	p, _ := store.Product("t200", "p1")
	if p.Card.Publication.Version != 1 {
		t.Fatal("тест должен сохранять давнюю версию публикации")
	}
}

func completeCardTasks(t *testing.T, store *fake.Store, now time.Time, delay time.Duration, want int) {
	t.Helper()
	count := 0
	for _, row := range store.OutboxRows() {
		if row.Kind != ports.TaskStoreCard || row.DoneAt != nil {
			continue
		}
		count++
		if !row.NextAt.Equal(now.Add(delay)) {
			t.Fatal("не применена задержка сводки")
		}
		if err := store.Complete(context.Background(), row.ID, now); err != nil {
			t.Fatal(err)
		}
	}
	if count != want {
		t.Fatalf("сводки: %d, ожидалось %d", count, want)
	}
}

func TestParentChangeRefreshesStoreCards(t *testing.T) {
	store := fake.NewStore()
	seedValues(t, store, "v1")
	clock := fake.NewClock(time.Now())
	service := apptaxonomy.NewService(store, clock, nil, time.Minute)
	d := schema(1)
	d.Categories = []taxonomy.Category{
		{ID: "r1", Name: "Первый", Slug: "r1", Children: d.Categories},
		{ID: "r2", Name: "Второй", Slug: "r2", Schema: "s1"},
	}
	if err := applyDefinition(context.Background(), service, d); err != nil {
		t.Fatal(err)
	}
	runRevalidation(t, service, 1)
	// У товара нет пути категории: задачу должна поставить именно проверка дерева.
	store.SeedProduct(catalog.ProductState{
		TenantID:  "t1",
		ProductID: "p1",
		Version:   1,
		Card:      catalog.Card{Publication: d.Check(nil)},
	})
	d.Categories[1].Children = d.Categories[0].Children
	d.Categories[0].Children = nil
	d.Categories[0].Schema = "s1"
	d.Categories[1].Schema = ""
	if _, err := service.LoadPart(context.Background(), "categories", d, 1); err != nil {
		t.Fatal(err)
	}
	runRevalidation(t, service, 2)
	completeCardTasks(t, store, clock.Now(), time.Minute, 1)
	p, _ := store.Product("t1", "p1")
	if p.Version != 1 {
		t.Fatal("неизменная публикация сохранена")
	}
}

func TestPartialRenameRollbackRefreshesCards(t *testing.T) {
	for _, singleStore := range []bool{false, true} {
		t.Run(fmt.Sprint(singleStore), func(t *testing.T) {
			store := fake.NewStore()
			seedValues(t, store, "v1")
			clock := fake.NewClock(time.Now())
			service := apptaxonomy.NewService(store, clock, nil, 0)
			d := schema(1)
			d.Categories = []taxonomy.Category{{ID: "root", Name: "Корень", Slug: "root", Children: d.Categories}}
			if err := applyDefinition(context.Background(), service, d); err != nil {
				t.Fatal(err)
			}
			runRevalidation(t, service, 1)
			card := &taxonomy.Card{CategoryID: "c1", Published: true}
			for i := 0; i < 201; i++ {
				tenant := ids.TenantID(fmt.Sprintf("t%03d", i))
				if singleStore {
					tenant = "t1"
				}
				store.SeedProduct(catalog.ProductState{
					TenantID:  tenant,
					ProductID: ids.ProductID(fmt.Sprintf("p%03d", i)),
					Version:   1,
					Card:      catalog.Card{Marketplace: card, Publication: d.Check(card)},
				})
			}
			d.Categories[0].Name = "Новое имя"
			if _, err := service.LoadPart(context.Background(), "categories", d, 1); err != nil {
				t.Fatal(err)
			}
			runRevalidation(t, service, 2)
			progress := readProgress(t, store)
			if progress.Done || progress.CardsVersion != 1 {
				t.Fatalf("частичное освежение потеряно: %+v", progress)
			}
			want := 200
			if singleStore {
				want = 1
			}
			completeCardTasks(t, store, clock.Now(), 0, want)
			d.Categories[0].Name = "Корень"
			if _, err := service.LoadPart(context.Background(), "categories", d, 2); err != nil {
				t.Fatal(err)
			}
			if !readProgress(t, store).CardsDirty {
				t.Fatal("замещение потеряло признак частичного освежения")
			}
			runRevalidation(t, service, 3)
			completeCardTasks(t, store, clock.Now(), 0, want)
			runRevalidation(t, service, 3)
			want = 1
			if singleStore {
				want = 0
			}
			completeCardTasks(t, store, clock.Now(), 0, want)
			progress = readProgress(t, store)
			if !progress.Done || progress.CardsDirty || progress.CardsVersion != 3 {
				t.Fatalf("проход не завершён: %+v", progress)
			}
		})
	}
}
