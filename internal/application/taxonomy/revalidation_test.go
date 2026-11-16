package taxonomy_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestRevalidationResumesAndUsesLatestVersion(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	definition := revalidationSchema()
	store.SeedTaxonomy(definition)
	clock := fake.NewClock(time.Now())
	for i := 0; i < 120; i++ {
		seedRevalidationProduct(store, definition, ids.ProductID(fmt.Sprintf("p%03d", i)), "c1")
	}
	seedRevalidationProduct(store, definition, "unaffected", "c2")
	service := apptaxonomy.NewService(store, clock, nil)
	definition.Attributes[0].Name = "Изменённое поле"
	if _, err := service.LoadPart(ctx, "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	if len(store.OutboxRows()) != 0 {
		t.Fatal("публикация обошла товары")
	}
	job, err := service.RevalidateNext(ctx)
	if err != nil || job == nil || job.Processed != 50 || job.Done {
		t.Fatal(job, err)
	}
	rows := len(store.OutboxRows())
	store.FailCommit = errors.New("сбой страницы")
	if job, err := service.RevalidateNext(ctx); err == nil || job != nil {
		t.Fatal("ошибка страницы не возвращена", job, err)
	}
	store.FailCommit = nil
	if len(store.OutboxRows()) != rows {
		t.Fatal("сбой страницы оставил задачи")
	}
	if state, _ := store.Product("t1", "p050"); state.Card.Publication.Version != 1 {
		t.Fatal("сбой страницы сохранил товар", state)
	}
	// Событие между страницами сразу использует новую голову, включая ключ позади курсора.
	state, _ := store.Product("t1", "p000")
	projector := appcatalog.NewProjector(store, clock, nil)
	if err := projector.ApplyProduct(ctx, appcatalog.ProductEvent{
		EventID: "new-event",
		Snapshot: catalog.Snapshot{
			TenantID:  state.TenantID,
			ProductID: state.ProductID,
			Revision:  2,
			At:        clock.Now(),
			Card:      state.Card,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if state, _ := store.Product("t1", "p000"); state.Revision != 2 || state.Card.Publication.Version != 2 {
		t.Fatal("событие не принято по новой версии", state)
	}
	definition.Attributes[0].Name = "Следующая версия"
	if _, err := service.LoadPart(ctx, "attributes", definition, 2); err != nil {
		t.Fatal(err)
	}
	// Новый экземпляр после сбоя продолжает сохранённый курсор; старое задание проверяет уже версию 3.
	service = apptaxonomy.NewService(store, clock, nil)
	job, err = service.RevalidateNext(ctx)
	if err != nil || job == nil || job.Version != 2 || job.Processed != 100 {
		t.Fatal("прогресс потерян", job, err)
	}
	if state, _ := store.Product("t1", "p050"); state.Card.Publication.Version != 3 {
		t.Fatal("старое задание вернуло старую версию", state)
	}
	drainRevalidation(t, service)
	for i := 0; i < 120; i++ {
		state, _ := store.Product("t1", ids.ProductID(fmt.Sprintf("p%03d", i)))
		if state.Card.Publication.Version != 3 {
			t.Fatal("страница потеряла товар", state)
		}
	}
	if state, _ := store.Product("t1", "unaffected"); state.Version != 1 || state.Card.Publication.Version != 1 {
		t.Fatal("затронута посторонняя схема", state)
	}
	rows = len(store.OutboxRows())
	drainRevalidation(t, service)
	if len(store.OutboxRows()) != rows {
		t.Fatal("завершённая перепроверка повторилась")
	}
}

func revalidationSchema() taxonomy.Definition {
	d := schema(1)
	d.Attributes = []taxonomy.Attribute{
		{Code: "a1", Name: "Поле", Type: "string"},
		{Code: "a2", Name: "Другое поле", Type: "string"},
	}
	d.Schemas["s2"] = taxonomy.Schema{Attributes: []string{"a2"}, Optional: []string{"a2"}}
	d.Categories = append(d.Categories, taxonomy.Category{ID: "c2", Slug: "c2", Name: "Другая", Schema: "s2"})
	return d
}

func seedRevalidationProduct(store *fake.Store, d taxonomy.Definition, id ids.ProductID, category string) {
	card := &taxonomy.Card{CategoryID: category, Published: true}
	store.SeedProduct(catalog.ProductState{
		TenantID:  "t1",
		ProductID: id,
		Revision:  1,
		Version:   1,
		Card: catalog.Card{
			Name:        "Товар",
			Visible:     true,
			Marketplace: card,
			Publication: d.Check(card),
		},
	})
}
