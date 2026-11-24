//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type concurrentModelResolver struct {
	once   sync.Once
	update func() error
	err    error
}

type conflictObserver struct {
	conflicts int
}

func TestModelKeysStoredAndReportedAcrossSellers(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	for _, id := range []ids.TenantID{"one", "two"} {
		seedTenant(t, uow, id, string(id), participant.TenantActive)
		err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			tenant, err := tx.Registry().Get(ctx, id)
			if err != nil {
				return err
			}
			seller, err := participant.New(tenant, "Магазин", at(1))
			if err != nil {
				return err
			}
			if _, err := seller.ApplyShopSettings(
				w3RepositoriesIntegrationDelivery,
				at(2),
			); err != nil {
				return err
			}
			if _, err := seller.ApplyStoreCard(
				participant.Profile{},
				participant.CardReadiness{Ready: true},
				nil,
				at(3),
			); err != nil {
				t.Fatal(err)
			}
			if _, err := seller.Enable(at(3)); err != nil {
				t.Fatal(err)
			}
			if err := tx.Participants().Add(ctx, seller); err != nil {
				return err
			}
			snap := snapshot(id, "product", 1, 0)
			snap.Card.Publication.Published = true
			snap.Card.Publication.CategoryPath = []string{"direction"}
			snap.Card.Publication.Schema = "s1"
			snap.Card.Publication.Issues = []taxonomy.Issue{{Code: "identity_incomplete", Attribute: "axis"}}
			snap.Card.Variants = &catalog.Variants{
				GroupID: "v1",
				Axes: []catalog.VariantAxis{
					{
						Key:   "platform.color",
						Code:  "color",
						Name:  "Цвет",
						Value: "Синий",
					},
				},
				Position: 2,
			}
			product, err := catalog.NewProduct(snap, at(0))
			if err != nil {
				return err
			}
			product.SetIdentityKeys("s1:brand-id:a1", "identity", "exact:0036000291452")
			return tx.Products().Save(ctx, product)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	report, err := postgres.NewModelKeyStats(postgres.Single(pool)).Report(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Samples) != 1 || len(report.Counts) != 3 {
		t.Fatalf("отчёт: %+v", report)
	}
	for _, count := range report.Counts {
		if count.Offers != 2 || count.Shared != 1 || count.Share != 1 ||
			count.IdentityIncomplete != 2 || count.IdentityIncompleteShare != 1 {
			t.Fatalf("счётчики: %+v", count)
		}
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		product, err := tx.Products().Get(ctx, "one", "product")
		if err != nil {
			return err
		}
		state := product.State()
		if state.Card.ModelKey != "s1:brand-id:a1" || state.Card.VariantKey != "exact:0036000291452" ||
			state.Card.Variants == nil || state.Card.Variants.Position != 2 {
			return fmt.Errorf("потерян контракт карточки: %+v", state.Card)
		}
		product.SetModelKey("", "")
		return tx.Products().Save(ctx, product)
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err = postgres.NewModelKeyStats(postgres.Single(pool)).Report(ctx, true)
	if err != nil || len(report.Samples) != 0 {
		t.Fatalf("откат ключа: %+v, %v", report, err)
	}
}

// Конфликт возникает в настоящем PostgreSQL между чтением индексатором и Save.

func (r *concurrentModelResolver) Resolve(context.Context, string) (string, bool, error) {
	r.once.Do(func() {
		r.err = r.update()
	})
	return "brand-id", true, r.err
}

func (o *conflictObserver) ResolveFailed() {}
func (o *conflictObserver) Conflict() {
	o.conflicts++
}

func TestModelKeyConflictDoesNotAbortRebuild(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	ctx := context.Background()
	seedTenant(t, uow, "one", "one", participant.TenantActive)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		current, err := tx.Taxonomy().Current(ctx)
		if err != nil {
			return err
		}
		definition := taxonomy.Definition{
			Version:    current.Version + 1,
			Categories: []taxonomy.Category{{ID: "c1", Name: "Категория", Slug: "c1", Schema: "s1"}},
			Attributes: []taxonomy.Attribute{
				{Code: "brand", Name: "Бренд", Type: "string", Dictionary: "brand"},
				{Code: "part_number", Name: "Артикул", Type: "string"},
			},
			Schemas: map[string]taxonomy.Schema{
				"s1": {
					Attributes: []string{"brand", "part_number"},
					Identity:   &taxonomy.SchemaIdentity{Model: []string{"brand", "part_number"}},
				},
			},
		}
		if err := tx.Taxonomy().Save(ctx, definition, current.Version); err != nil {
			return err
		}
		tenant, err := tx.Registry().Get(ctx, "one")
		if err != nil {
			return err
		}
		seller, err := participant.New(tenant, "Магазин", at(1))
		if err != nil {
			return err
		}
		if _, err := seller.ApplyShopSettings(
			w3RepositoriesIntegrationDelivery,
			at(2),
		); err != nil {
			return err
		}
		if _, err := seller.ApplyStoreCard(
			participant.Profile{},
			participant.CardReadiness{Ready: true},
			nil,
			at(3),
		); err != nil {
			t.Fatal(err)
		}
		if _, err := seller.Enable(at(3)); err != nil {
			t.Fatal(err)
		}
		if err := tx.Participants().Add(ctx, seller); err != nil {
			return err
		}
		for _, id := range []ids.ProductID{"first", "second"} {
			snap := snapshot("one", id, 1, 0)
			snap.Card.Publication.Published = true
			snap.Card.Publication.CategoryID = "c1"
			snap.Card.Publication.Attributes = map[string]json.RawMessage{
				"brand":       json.RawMessage(`"Brand"`),
				"part_number": json.RawMessage(`"A1"`),
			}
			product, err := catalog.NewProduct(snap, at(0))
			if err != nil {
				return err
			}
			if err := tx.Products().Save(ctx, product); err != nil {
				return err
			}
			// Без вердикта модерации предложение скрыто: пересборке нужен допуск.
			verdict := moderation.NewVerdict(snap.TenantID, snap.ProductID)
			verdict.Evaluate(snap.Revision, 0, moderation.Rules{}, moderation.Input{}, at(0))
			if err := tx.ModerationVerdicts().Save(ctx, verdict); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	resolver := &concurrentModelResolver{
		update: func() error {
			return uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				product, err := tx.Products().Get(ctx, "one", "first")
				if err != nil {
					return err
				}
				snap := snapshot("one", "first", 2, 4)
				snap.Card = product.State().Card
				snap.Card.Name = "Новая карточка"
				if _, err := product.Apply(snap, at(4)); err != nil {
					return err
				}
				return tx.Products().Save(ctx, product)
			})
		},
	}
	observer := &conflictObserver{}
	index := fake.NewIndex()
	indexer := appcatalog.NewIndexer(
		uow,
		index,
		fake.NewClock(at(3)),
		1,
		&fake.IndexRebuildLock{},
		appcatalog.NewModelKeys(resolver, observer, true),
		30*time.Second,
	)
	if _, err := indexer.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if resolver.err != nil || observer.conflicts != 1 {
		t.Fatalf("конфликт не учтён: %d, %v", observer.conflicts, resolver.err)
	}
	first, ok := index.Doc("one~first")
	if !ok || first.Name != "Новая карточка" || first.ModelKey != "s1:brand-id:a1" {
		t.Fatalf("догоняющий проход не восстановил актуальный снимок: %+v", first)
	}
	if _, ok := index.Doc("one~second"); !ok {
		t.Fatal("конфликт остановил обработку следующей записи")
	}
}

func TestProjectorDoesNotResolveModelInsideTransaction(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	// Даже при недоступном реестре расчёт принадлежит только индексатору.
	// Проектор сохраняет атрибуты и outbox без зависимости от BrandResolver.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	snap := snapshot("one", "product", 1, 0)
	snap.Card.Publication.Attributes = map[string]json.RawMessage{
		"brand":       json.RawMessage(`"Unavailable brand"`),
		"part_number": json.RawMessage(`"A1"`),
	}
	projector := appcatalog.NewProjector(uow, fake.NewClock(at(1)), nil)
	if err := projector.ApplyProduct(ctx, appcatalog.ProductEvent{
		Snapshot: snap,
	}); err != nil {
		t.Fatal(err)
	}
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		product, err := tx.Products().Get(ctx, "one", "product")
		if err != nil {
			return err
		}
		if product.State().Card.ModelKey != "" {
			return fmt.Errorf("%+v", "проектор не должен вычислять ключ модели")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
