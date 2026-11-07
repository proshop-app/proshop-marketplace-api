package taxonomy_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestPublicationOnlySchedulesLatestVersion(t *testing.T) {
	store := fake.NewStore()
	seedValues(t, store, "v1")
	service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil, 0)
	store.SeedProduct(catalog.ProductState{TenantID: "t1", ProductID: "p1", Version: 1})
	if err := applyDefinition(context.Background(), service, schema(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.LoadPart(context.Background(), "categories", schema(2), 1); err != nil {
		t.Fatal(err)
	}
	if store.ProductPageCalls() != 0 {
		t.Fatal("публикация читала каталог")
	}
	rows := store.OutboxRows()
	if len(rows) != 1 || rows[0].Kind != apptaxonomy.TaskRevalidate || string(rows[0].Payload) != `{"version":2}` {
		t.Fatalf("ожидалась одна задача последней версии: %+v", rows)
	}
	p, _ := store.Product("t1", "p1")
	if p.Version != 1 || p.Card.Publication.Version != 0 {
		t.Fatal("публикация обошла товары")
	}
	runRevalidation(t, service, 1)
	p, _ = store.Product("t1", "p1")
	if p.Version != 1 {
		t.Fatal("устаревшая задача изменила товар")
	}
}

func TestRevalidationSkipsUnchangedAndAlreadyChecked(t *testing.T) {
	store := fake.NewStore()
	seedValues(t, store, "v1")
	clock := fake.NewClock(time.Now())
	evaluator := &fake.TaxonomyEvaluator{}
	service := apptaxonomy.NewService(store, clock, evaluator, 0)
	old := schema(1)
	for _, id := range []ids.ProductID{"same", "checked", "deleted"} {
		publication := old.Check(nil)
		var marketplace *taxonomy.Card
		if id == "checked" {
			publication.Version = 2
			marketplace = &taxonomy.Card{CategoryID: "c1", Published: true}
		}
		store.SeedProduct(catalog.ProductState{
			TenantID:  "t1",
			ProductID: id,
			Version:   1,
			Deleted:   id == "deleted",
			Card:      catalog.Card{Publication: publication, Marketplace: marketplace},
		})
	}
	if err := applyDefinition(context.Background(), service, old); err != nil {
		t.Fatal(err)
	}
	runRevalidation(t, service, 1)
	completeRevalidationTasks(t, store, clock.Now())
	if _, err := service.LoadPart(context.Background(), "categories", old, 1); err != nil {
		t.Fatal(err)
	}
	runRevalidation(t, service, 2)
	runRevalidation(t, service, 2)
	pending := 0
	for _, row := range store.OutboxRows() {
		if row.DoneAt == nil {
			pending++
			if row.Kind != apptaxonomy.TaskRevalidate {
				t.Fatalf("неожиданная задача: %s", row.Kind)
			}
		}
	}
	if len(evaluator.Calls) != 0 || pending != 1 {
		t.Fatal("неизменные товары вызвали побочные эффекты")
	}
	for _, id := range []ids.ProductID{"same", "checked", "deleted"} {
		p, _ := store.Product("t1", id)
		if p.Version != 1 {
			t.Fatal("неизменный товар сохранён")
		}
	}
}

func TestRevalidationResumesAndNewVersionRestarts(t *testing.T) {
	for _, supersede := range []bool{false, true} {
		t.Run(fmt.Sprint(supersede), func(t *testing.T) {
			store := fake.NewStore()
			seedValues(t, store, "v1")
			evaluator := &fake.TaxonomyEvaluator{
				FailOffer: ids.OfferID{Tenant: "t2", Product: "p000"},
				Err:       errors.New("сбой второй пачки"),
			}
			service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), evaluator, 0)
			for i := 0; i < 205; i++ {
				tenant, product := ids.TenantID("t1"), ids.ProductID(fmt.Sprintf("p%03d", i))
				if i >= 200 {
					tenant, product = "t2", ids.ProductID(fmt.Sprintf("p%03d", i-200))
				}
				store.SeedProduct(catalog.ProductState{TenantID: tenant, ProductID: product, Version: 1})
			}
			if err := applyDefinition(context.Background(), service, schema(1)); err != nil {
				t.Fatal(err)
			}
			task := ports.Task{Kind: apptaxonomy.TaskRevalidate, Payload: []byte(`{"version":1}`)}
			runRevalidation(t, service, 1)
			if err := service.HandleTask(context.Background(), task); !errors.Is(err, evaluator.Err) {
				t.Fatal(err)
			}
			progress := readProgress(t, store)
			if progress.TenantID != "t1" || progress.ProductID != "p199" || progress.Done {
				t.Fatalf("потерян курсор первой пачки: %+v", progress)
			}
			p, _ := store.Product("t2", "p000")
			if p.Version != 1 {
				t.Fatal("сбой не откатил пачку")
			}
			if store.ProductPageCalls() != 1 {
				t.Fatal("не зафиксирована ровно одна пачка")
			}
			evaluator.Err = nil
			version := int64(1)
			if supersede {
				if _, err := service.LoadPart(context.Background(), "categories", schema(1), 1); err != nil {
					t.Fatal(err)
				}
				version = 2
				runRevalidation(t, service, 1)
				if p := readProgress(t, store); p.TenantID != "" || p.Done {
					t.Fatalf("старая задача сдвинула новый курсор: %+v", p)
				}
			}
			runRevalidation(t, service, version)
			runRevalidation(t, service, version)
			expectedPages := 2
			if supersede {
				expectedPages = 3
			}
			if store.ProductPageCalls() != expectedPages {
				t.Fatal("повтор перечитал завершённые страницы")
			}
			if len(evaluator.Calls) != 206 || !readProgress(t, store).Done {
				t.Fatalf("повторная обработка уже сохранённых товаров: %d", len(evaluator.Calls))
			}
		})
	}
}

func readProgress(t *testing.T, store *fake.Store) ports.TaxonomyProgress {
	t.Helper()
	var progress ports.TaxonomyProgress
	if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		var err error
		progress, err = tx.TaxonomyRevalidation().LockProgress(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return progress
}

func TestCategoryIndexFieldsWithoutModerationChange(t *testing.T) {
	for _, change := range []string{"name", "title", "path", "unrelated", "remove-unrelated"} {
		t.Run(change, func(t *testing.T) {
			store := fake.NewStore()
			seedValues(t, store, "v1")
			evaluator := &fake.TaxonomyEvaluator{}
			clock := fake.NewClock(time.Now())
			service := apptaxonomy.NewService(store, clock, evaluator, 0)
			definition := schema(1)
			if change == "remove-unrelated" {
				definition.Categories = append(definition.Categories, taxonomy.Category{
					ID:     "c2",
					Slug:   "c2",
					Name:   "Другая категория",
					Schema: "s1",
				})
			}
			card := &taxonomy.Card{CategoryID: "c1", Published: true}
			store.SeedProduct(catalog.ProductState{
				TenantID:  "t1",
				ProductID: "p1",
				Version:   1,
				Card:      catalog.Card{Marketplace: card, Publication: definition.Check(card)},
			})
			if err := applyDefinition(context.Background(), service, definition); err != nil {
				t.Fatal(err)
			}
			runRevalidation(t, service, 1)
			completeRevalidationTasks(t, store, clock.Now())
			switch change {
			case "name":
				definition.Categories[0].Name = "Новое имя"
			case "title":
				definition.Categories[0].Title = "Полное имя"
			case "path":
				definition.Categories = []taxonomy.Category{
					{ID: "root", Slug: "root", Name: "Корень", Children: definition.Categories},
				}
			case "remove-unrelated":
				definition.Categories = definition.Categories[:1]
			case "unrelated":
				definition.Categories = append(definition.Categories, taxonomy.Category{
					ID:     "c2",
					Slug:   "c2",
					Name:   "Другая категория",
					Schema: "s1",
				})
			}
			if _, err := service.LoadPart(context.Background(), "categories", definition, 1); err != nil {
				t.Fatal(err)
			}
			runRevalidation(t, service, 2)
			expected := 1
			if change == "title" {
				expected = 0
			}
			completeCardTasks(t, store, clock.Now(), 0, expected)
			if len(evaluator.Calls) != 0 {
				t.Fatalf("лишние оценки: %d", len(evaluator.Calls))
			}
			p, _ := store.Product("t1", "p1")
			if change == "unrelated" || change == "remove-unrelated" {
				if p.Version != 1 {
					t.Fatal("неизменный товар сохранён")
				}
			} else {
				definition.Version = 2
				want := definition.Check(card)
				if p.Version != 2 || !reflect.DeepEqual(p.Card.Publication, want) {
					t.Fatalf("публикация не сохранена: %+v, ожидалась %+v", p, want)
				}
				found := false
				for _, row := range store.OutboxRows() {
					found = found || row.Kind == appcatalog.TaskSyncOffer
				}
				if !found {
					t.Fatal("не поставлена синхронизация")
				}
			}
		})
	}
}

func TestEffectivePolicyChangeReevaluatesPublication(t *testing.T) {
	for _, oldPolicy := range []string{"auto", "forbidden"} {
		t.Run(oldPolicy, func(t *testing.T) {
			store := fake.NewStore()
			seedValues(t, store, "v1")
			evaluator := &fake.TaxonomyEvaluator{}
			service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), evaluator, 0)
			definition := schema(1)
			definition.Categories[0].Moderation = oldPolicy
			if err := applyDefinition(context.Background(), service, definition); err != nil {
				t.Fatal(err)
			}
			setCardsVersion(t, store, 1)
			card := &taxonomy.Card{CategoryID: "c1", Published: true}
			store.SeedProduct(catalog.ProductState{
				TenantID:  "t1",
				ProductID: "p1",
				Version:   1,
				Card:      catalog.Card{Marketplace: card, Publication: definition.Check(card)},
			})
			nextPolicy := "auto"
			if oldPolicy == "auto" {
				nextPolicy = "forbidden"
			}
			// Политика наследуется, не только задаётся непосредственно на листе.
			definition.Categories[0].Moderation = ""
			definition.Categories = []taxonomy.Category{
				{ID: "root", Slug: "root", Name: "Корень", Moderation: nextPolicy, Children: definition.Categories},
			}
			if _, err := service.LoadPart(context.Background(), "categories", definition, 1); err != nil {
				t.Fatal(err)
			}
			runRevalidation(t, service, 2)
			runRevalidation(t, service, 2)
			if len(evaluator.Calls) != 1 || len(store.OutboxRows()) != 4 {
				t.Fatalf("смена политики: оценки=%d задачи=%d", len(evaluator.Calls), len(store.OutboxRows()))
			}
		})
	}
}

func setCardsVersion(t *testing.T, store *fake.Store, version int64) {
	t.Helper()
	if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.TaxonomyRevalidation().LockProgress(ctx)
		if err != nil {
			return err
		}
		p.CardsVersion = version
		return tx.TaxonomyRevalidation().SaveProgress(ctx, p)
	}); err != nil {
		t.Fatal(err)
	}
}

func completeRevalidationTasks(t *testing.T, store *fake.Store, now time.Time) {
	t.Helper()
	if !readProgress(t, store).Done {
		t.Fatal("проход не завершён")
	}
	for _, row := range store.OutboxRows() {
		if row.DoneAt == nil {
			if err := store.Complete(context.Background(), row.ID, now); err != nil {
				t.Fatal(err)
			}
		}
	}
}
