//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"golang.org/x/sync/errgroup"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	app "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestStoreSummaryDeltas(t *testing.T) {
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(testPool(t))
	clock := fake.NewClock(at(12))
	seedSummarySeller(t, uow, clock)
	service := app.NewService(uow, clock, &fake.IDs{})
	projector := appcatalog.NewProjector(uow, clock, nil)
	p1 := summarySnapshot("p1", "c1", "v1", 100, 5, 10)
	p2 := summarySnapshot("p2", "c1", "v1", 200, 4, 20)
	p3 := summarySnapshot("p3", "c2", "", 300, 2, 10)
	for _, p := range []catalog.Snapshot{p1, p2, p3} {
		applySummarySnapshot(t, projector, p)
	}
	allowSummaryProducts(t, uow, "p1", "p2", "p3")
	summary := refreshSummary(t, service)
	assertDeltaSummary(t, summary, 3, 100, 3.5, 20, "p1")

	p3.Revision++
	p3.At = at(3)
	p3.Card.Name = "Новое имя"
	p3.Card.Image = "/new.png"
	p3.Card.Marketplace.CategoryID = "c1"
	applySummarySnapshot(t, projector, p3)
	summary = refreshSummary(t, service)
	assertDeltaSummary(t, summary, 3, 100, 3.5, 20, "p3")
	if top := summary.Products[0]; top.Name != p3.Card.Name || top.Image == nil || *top.Image != p3.Card.Image {
		t.Fatalf("витрина не приняла карточку: %+v", top)
	}

	if len(summary.Categories) != 1 || summary.Categories[0].ID != "c1" || summary.Categories[0].Count != 3 {
		t.Fatalf("витрина не перенесла товар между категориями: %+v", summary.Categories)
	}

	p3.Revision++
	p3.At = at(4)
	p3.RatingsCount = 30
	p3.AverageRating = 3
	applySummarySnapshot(t, projector, p3)
	assertDeltaSummary(t, refreshSummary(t, service), 3, 100, 3.5, 40, "p3")

	// Откат обработки оставляет дельты; конкурентная запись не ждёт их фиксации.
	p1.Revision++
	p1.Deleted = true
	p1.At = at(5)
	applySummarySnapshot(t, projector, p1)
	p2.Revision++
	p2.At = at(5)
	p2.Price = money.FromKopecks(180)
	p2.BasePrice = p2.Price
	applySummarySnapshot(t, projector, p2)
	p2.Revision++
	p2.At = at(6)
	p2.Price = money.FromKopecks(150)
	p2.BasePrice = p2.Price
	rollback := errors.New("откат фоновой транзакции")
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.StoreCards().Lock(ctx, "t1"); err != nil {
			return err
		}
		if _, err := tx.StoreCards().Rollup(ctx, "t1", true, []string{"c1", "c2"}); err != nil {
			return err
		}
		workers, work := errgroup.WithContext(ctx)
		workers.Go(func() error {
			return projector.ApplyProduct(work, appcatalog.ProductEvent{Snapshot: p2})
		})
		workers.Go(func() error {
			return reviewSummaryProduct(work, uow, p3.ProductID, p3.Revision)
		})
		if err := workers.Wait(); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	for _, p := range []catalog.Snapshot{p1, p2, p3} {
		applySummarySnapshot(t, projector, p)
	}
	task := ports.Task{Kind: ports.TaskStoreCard, TenantID: "t1"}
	var workers errgroup.Group
	for range 2 {
		workers.Go(func() error { return service.RefreshStoreCard(ctx, task) })
	}
	if err := workers.Wait(); err != nil {
		t.Fatal(err)
	}
	summary, err = service.StoreSummary(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	assertDeltaSummary(t, summary, 1, 150, 4, 20, "p2")
	if len(summary.Categories) != 1 || summary.Categories[0].ID != "c1" || summary.Categories[0].Count != 1 {
		t.Fatalf("неверная дельта категорий: %+v", summary.Categories)
	}
	repeated := refreshSummary(t, service)
	assertDeltaSummary(t, repeated, 1, 150, 4, 20, "p2")

	p2.Revision++
	p2.At = at(7)
	p2.Card.Visible = false
	applySummarySnapshot(t, projector, p2)
	hidden := refreshSummary(t, service)
	if hidden.Offers != 2 || hidden.FromPrice == nil || *hidden.FromPrice != 150 ||
		len(hidden.Products) != 0 || hidden.Rating != nil {
		t.Fatalf("после скрытия видимых товаров нужна статистика кандидатов: %+v", hidden)
	}

}

func TestStoreSummaryKeepsEarlierUncommittedChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	uow := postgres.NewUnitOfWork(testPool(t))
	clock := fake.NewClock(at(12))
	seedSummarySeller(t, uow, clock)
	service := app.NewService(uow, clock, &fake.IDs{})
	projector := appcatalog.NewProjector(uow, clock, nil)
	p1 := summarySnapshot("p1", "c1", "", 100, 0, 0)
	p2 := summarySnapshot("p2", "c1", "", 200, 0, 0)
	applySummarySnapshot(t, projector, p1)
	applySummarySnapshot(t, projector, p2)
	allowSummaryProducts(t, uow, "p1", "p2")
	refreshSummary(t, service)

	p1.Revision++
	p1.At = at(1)
	p1.Price = money.FromKopecks(50)
	p1.BasePrice = p1.Price
	saved, commit := make(chan struct{}), make(chan struct{})
	worker, work := errgroup.WithContext(ctx)
	worker.Go(func() error {
		return uow.Do(work, func(ctx context.Context, tx ports.Tx) error {
			p, err := tx.Products().Get(ctx, p1.TenantID, p1.ProductID)
			if err != nil {
				return err
			}
			p1.Card.Publication = p.State().Card.Publication
			if _, err := p.Apply(p1, clock.Now()); err != nil {
				return err
			}
			if err := tx.Products().Save(ctx, p); err != nil {
				return err
			}
			close(saved)
			select {
			case <-commit:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	})
	defer func() {
		cancel()
		_ = worker.Wait()
	}()
	select {
	case <-saved:
	case <-work.Done():
		t.Fatal(worker.Wait())
	}

	p2.Revision++
	p2.At = at(2)
	p2.Price = money.FromKopecks(90)
	p2.BasePrice = p2.Price
	applySummarySnapshot(t, projector, p2)
	later := refreshSummary(t, service)
	if later.FromPrice == nil || *later.FromPrice != 90 {
		t.Fatalf("не применилось зафиксированное изменение второго товара: %+v", later)
	}
	close(commit)
	if err := worker.Wait(); err != nil {
		t.Fatal(err)
	}

	both := refreshSummary(t, service)
	if both.FromPrice == nil || *both.FromPrice != 50 {
		t.Fatalf("позиция обработки пропустила раннюю транзакцию: %+v", both)
	}
}

func TestStoreSummaryDrainsLateSourceInPages(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	clock := fake.NewClock(at(12))
	seedSummarySeller(t, uow, clock)
	service := app.NewService(uow, clock, &fake.IDs{})
	projector := appcatalog.NewProjector(uow, clock, nil)

	older, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = older.Rollback(ctx) }()
	const lateProducts = 450
	if _, err := older.Exec(ctx, seedSummaryScaleProducts, 1, lateProducts, at(0), "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := older.Exec(ctx, seedSummaryScaleVerdicts, "p000001", "p000450", at(0)); err != nil {
		t.Fatal(err)
	}
	applySummarySnapshot(t, projector, summarySnapshot("new", "c1", "", 200, 0, 0))
	allowSummaryProducts(t, uow, "new")
	if summary := refreshSummary(t, service); summary.Offers != 1 {
		t.Fatalf("сводка не приняла зафиксированный товар: %+v", summary)
	}

	if err := older.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if summary := refreshSummary(t, service); summary.Offers != 1 {
		t.Fatalf("первая порция преждевременно обновила готовую сводку: %+v", summary)
	}
	drainSummaryRollup(t, uow)
	summary := refreshSummary(t, service)
	if summary.Offers != lateProducts+1 || summary.FromPrice == nil || *summary.FromPrice != 100 {
		t.Fatalf("поздняя транзакция обработана не полностью: %+v", summary)
	}
}

func TestStoreSummaryMigrationQueuesExistingCatalog(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	if err := postgres.MigrateDownTo(ctx, pool, 79); err != nil {
		t.Fatal(err)
	}
	uow := postgres.NewUnitOfWork(pool)
	clock := fake.NewClock(at(12))
	seedSummarySeller(t, uow, clock)
	p := summarySnapshot("p1", "c1", "", 100, 4, 10)
	applySummarySnapshot(t, appcatalog.NewProjector(uow, clock, nil), p)
	allowSummaryProducts(t, uow, "p1")
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	service := app.NewService(uow, clock, &fake.IDs{})
	var pending int
	query := `SELECT count(*) FROM outbox WHERE kind = 'participant.store-card.refresh' AND tenant_id = 't1'`
	if err := pool.QueryRow(ctx, query).Scan(&pending); err != nil || pending != 1 {
		t.Fatal(pending, err)
	}
	summary := refreshSummary(t, service)
	assertDeltaSummary(t, summary, 1, 100, 4, 10, "p1")
}

func seedSummarySeller(t *testing.T, uow *postgres.UnitOfWork, clock ports.Clock) {
	t.Helper()
	ctx := context.Background()
	definition := testTaxonomy(1)
	definition.Categories = append(definition.Categories, taxonomy.Category{
		ID:     "c2",
		Slug:   "c2",
		Name:   "Категория 2",
		Schema: "s1",
	})
	if err := applyDefinition(ctx, apptaxonomy.NewService(uow, clock, nil), definition); err != nil {
		t.Fatal(err)
	}
	seedTenant(t, uow, "t1", "shop", participant.TenantActive)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		p, err := participant.New(tenant, "Магазин", clock.Now())
		if err != nil {
			return err
		}
		if _, err := p.ApplyShopSettings(w3RepositoriesIntegrationDelivery, clock.Now()); err != nil {
			return err
		}
		profile := participant.Profile{}
		readiness := participant.CardReadiness{Ready: true}
		if _, err := p.ApplyStoreCard(profile, readiness, nil, clock.Now()); err != nil {
			return err
		}
		if _, err := p.Enable(clock.Now()); err != nil {
			return err
		}
		return tx.Participants().Add(ctx, p)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func summarySnapshot(
	id ids.ProductID,
	category string,
	group string,
	price int64,
	rating float64,
	ratings int64,
) catalog.Snapshot {
	p := snapshot("t1", id, 1, 0)
	p.Card.Marketplace = &taxonomy.Card{
		CategoryID: category,
		Published:  true,
		Attributes: map[string]json.RawMessage{"a1": json.RawMessage("1")},
	}
	p.Card.VariantsID = group
	p.Price = money.FromKopecks(price)
	p.BasePrice = p.Price
	p.AverageRating = rating
	p.RatingsCount = ratings
	return p
}

func applySummarySnapshot(t *testing.T, projector *appcatalog.Projector, p catalog.Snapshot) {
	t.Helper()
	if err := projector.ApplyProduct(context.Background(), appcatalog.ProductEvent{Snapshot: p}); err != nil {
		t.Fatal(err)
	}
}

func allowSummaryProducts(t *testing.T, uow *postgres.UnitOfWork, products ...ids.ProductID) {
	t.Helper()
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		for _, product := range products {
			v := moderation.NewVerdict("t1", product)
			v.Evaluate(1, 1, moderation.Rules{}, moderation.Input{}, at(0))
			if err := tx.ModerationVerdicts().Save(ctx, v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func reviewSummaryProduct(ctx context.Context, uow *postgres.UnitOfWork, product ids.ProductID, revision int64) error {
	return uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		v, err := tx.ModerationVerdicts().Get(ctx, "t1", product)
		if err != nil {
			return err
		}
		v.Review(revision, moderation.Actor{ID: "o1"}, at(6))
		return tx.ModerationVerdicts().Save(ctx, v)
	})
}

func refreshSummary(t *testing.T, service *app.Service) ports.StoreSummary {
	t.Helper()
	ctx := context.Background()
	if err := service.RefreshStoreCard(ctx, ports.Task{Kind: ports.TaskStoreCard, TenantID: "t1"}); err != nil {
		t.Fatal(err)
	}
	summary, err := service.StoreSummary(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	return summary
}

func assertDeltaSummary(
	t *testing.T,
	summary ports.StoreSummary,
	offers int,
	price int64,
	rating float64,
	ratings int64,
	lead string,
) {
	t.Helper()
	if summary.Offers != offers || summary.FromPrice == nil || *summary.FromPrice != price ||
		summary.Rating == nil || summary.Rating.Value != rating || summary.Rating.Count != ratings ||
		len(summary.Products) == 0 || summary.Products[0].ProductID != lead {
		t.Fatalf("неверная сводка: %+v, рейтинг %+v", summary, summary.Rating)
	}
}
