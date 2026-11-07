package taxonomy_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestSupersededPassRefreshesCardsAfterAddedRootRename(t *testing.T) {
	testSupersededCards(t, true)
}

func TestRevertBeforeFirstBatchRefreshesCards(t *testing.T) {
	testSupersededCards(t, false)
}

func testSupersededCards(t *testing.T, added bool) {
	t.Helper()
	ctx := context.Background()
	store := fake.NewStore()
	seedValues(t, store, "v1")
	clock := fake.NewClock(time.Now())
	service := apptaxonomy.NewService(store, clock, nil, 0)
	cards := appparticipant.NewService(store, clock, nil)
	d := schema(1)
	category := "c1"
	count := 3
	if added {
		category = "y"
		count = 201
	}
	if err := applyDefinition(ctx, service, d); err != nil {
		t.Fatal(err)
	}
	runRevalidation(t, service, 1)
	card := &taxonomy.Card{CategoryID: category, Published: true}
	for i := 0; i < count; i++ {
		tenant := ids.TenantID(fmt.Sprintf("t%03d", i))
		store.SeedTenant(participant.TenantState{TenantID: tenant, Status: participant.TenantActive})
		store.SeedParticipant(participant.State{TenantID: tenant, Version: 1})
		store.SeedProduct(catalog.ProductState{
			TenantID:  tenant,
			ProductID: "p1",
			Version:   1,
			PriceProfiles: []catalog.PriceProfile{{
				Key:            catalog.DefaultPriceProfile,
				Price:          money.FromKopecks(100),
				EffectivePrice: money.FromKopecks(100),
			}},
			Card: catalog.Card{Visible: true, Marketplace: card, Publication: d.Check(card)},
		})
		store.SeedVerdict(moderation.VerdictState{
			TenantID:  tenant,
			ProductID: "p1",
			Status:    moderation.Allowed,
			Version:   1,
		})
	}
	original := d.Categories[0].Name
	if added {
		d.Categories = append(d.Categories, taxonomy.Category{
			ID:       "x",
			Slug:     "x",
			Name:     "Новое имя",
			Children: []taxonomy.Category{{ID: "y", Slug: "y", Name: "Лист", Schema: "s1"}},
		})
	} else {
		d.Categories[0].Name = "Новое имя"
	}
	if _, err := service.LoadPart(ctx, "categories", d, 1); err != nil {
		t.Fatal(err)
	}
	if added {
		runRevalidation(t, service, 2)
	}
	refresh := func(want string) {
		t.Helper()
		if err := cards.RefreshStoreCard(ctx, ports.Task{Kind: ports.TaskStoreCard, TenantID: "t000"}); err != nil {
			t.Fatal(err)
		}
		summary, err := cards.StoreSummary(ctx, "t000")
		if err != nil || len(summary.Categories) != 1 || summary.Categories[0].Name != want {
			t.Fatalf("сводка: %+v, ошибка: %v", summary, err)
		}
	}
	refresh("Новое имя")
	// Завершённые сторонним обработчиком задачи не должны маскировать пропуск нового прохода.
	for _, row := range store.OutboxRows() {
		if row.Kind == ports.TaskStoreCard || row.Kind == "catalog.offer.sync" {
			if err := store.Complete(ctx, row.ID, clock.Now()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if added {
		d.Categories[1].Name = "Итоговое имя"
	} else {
		d.Categories[0].Name = original
	}
	if _, err := service.LoadPart(ctx, "categories", d, 2); err != nil {
		t.Fatal(err)
	}
	if progress := readProgress(t, store); !progress.CardsDirty || progress.CardsVersion != 1 {
		t.Fatalf("замещение не помечено до первой пачки: %+v", progress)
	}
	runRevalidation(t, service, 3)
	if added {
		runRevalidation(t, service, 3)
	}
	completeCardTasks(t, store, clock.Now(), 0, count)
	progress := readProgress(t, store)
	if !progress.Done || progress.CardsDirty || progress.CardsVersion != 3 {
		t.Fatalf("проход не завершён: %+v", progress)
	}
	if added {
		refresh("Итоговое имя")
	} else {
		refresh(original)
	}
}
