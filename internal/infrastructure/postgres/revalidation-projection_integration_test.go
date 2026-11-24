//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
)

func TestTaxonomyPageAndUnchangedProjectionPreserveCurrentPublication(t *testing.T) {
	for _, pageFirst := range []bool{true, false} {
		name := "событие до страницы"
		if pageFirst {
			name = "страница до события"
		}
		t.Run(name, func(t *testing.T) { checkRevalidationProjectionOrder(t, pageFirst) })
	}
}

func checkRevalidationProjectionOrder(t *testing.T, pageFirst bool) {
	t.Helper()
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(pgtest.Pool(t))
	service, definition := prepareModeratedRevalidation(t, uow, 1)
	evaluator := appmoderation.NewService(uow, clock.System{}, random.IDs{}, appmoderation.Config{})
	projector := appcatalog.NewProjector(uow, clock.System{}, evaluator)
	initial := revalidationProjectionProduct(t, uow)
	applyUnchangedRevalidationEvent(t, projector, initial.Card, 2)
	previous := revalidationProjectionProduct(t, uow)
	definition.Categories[0].Title = "Новое имя категории"
	if _, err := service.LoadPart(ctx, "categories", definition, 1); err != nil {
		t.Fatal(err)
	}
	if pageFirst {
		job, err := service.RevalidateNext(ctx)
		if err != nil || job == nil || job.Processed != 1 || !job.Done {
			t.Fatal(job, err)
		}
		assertRevalidationPublication(t, revalidationProjectionProduct(t, uow))
	}
	// Магазин присылает неизменное содержимое со старым производным Publication.
	applyUnchangedRevalidationEvent(t, projector, previous.Card, 3)
	afterEvent := revalidationProjectionProduct(t, uow)
	assertRevalidationPublication(t, afterEvent)
	drainTaxonomyPages(t, service)
	afterPage := revalidationProjectionProduct(t, uow)
	assertRevalidationPublication(t, afterPage)
	if afterPage.Version != afterEvent.Version || afterPage.Revision != 3 {
		t.Fatal("страница повторно проверила уже актуальную карточку", afterEvent, afterPage)
	}
}

func applyUnchangedRevalidationEvent(
	t *testing.T,
	projector *appcatalog.Projector,
	card catalog.Card,
	revision int64,
) {
	t.Helper()
	err := projector.ApplyProduct(context.Background(), appcatalog.ProductEvent{
		Snapshot: catalog.Snapshot{
			TenantID:  "t1",
			ProductID: "p000",
			Revision:  revision,
			At:        time.Now(),
			Card:      card,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func revalidationProjectionProduct(t *testing.T, uow ports.UnitOfWork) catalog.ProductState {
	t.Helper()
	var state catalog.ProductState
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		product, err := tx.Products().Get(ctx, "t1", "p000")
		if err == nil {
			state = product.State()
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func assertRevalidationPublication(t *testing.T, state catalog.ProductState) {
	t.Helper()
	publication := state.Card.Publication
	if publication.Version != 2 || publication.CategoryName != "Новое имя категории" || !publication.Published {
		t.Fatal("сохранена прежняя проверка вместо текущей", publication)
	}
}
