package taxonomy_test

import (
	"context"
	"encoding/json"
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

func schema(version int64) taxonomy.Definition {
	return taxonomy.Definition{Version: version, Categories: []taxonomy.Category{{
		ID:   "c1",
		Slug: "c1",
		Name: "Категория",
		Attributes: []taxonomy.Attribute{{
			Code:   "a1",
			Name:   "Атрибут",
			Type:   "enum",
			Values: []string{"v1"},
			Facet:  true,
		}},
	}}}
}

func TestLoadAndRevalidate(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	clock := fake.NewClock(time.Now())
	service := apptaxonomy.NewService(store, clock)
	projector := appcatalog.NewProjector(store, clock)
	if err := service.Load(ctx, schema(1)); err != nil {
		t.Fatal(err)
	}
	event := appcatalog.ProductEvent{EventID: "e1", Snapshot: catalog.Snapshot{
		TenantID:  "t1",
		ProductID: "p1",
		Revision:  1,
		At:        clock.Now(),
		Card: catalog.Card{Name: "Товар", Visible: true, Marketplace: &taxonomy.Card{
			CategoryID: "c1",
			Published:  true,
			Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`"v1"`)},
		}},
	}}
	if err := projector.ApplyProduct(ctx, event); err != nil {
		t.Fatal(err)
	}
	p, _ := store.Product("t1", "p1")
	if !p.Card.Publication.Published || p.Card.Publication.Version != 1 {
		t.Fatalf("%+v", p)
	}
	if err := service.Load(ctx, schema(1)); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := store.Product("t1", "p1")
	if unchanged.Version != p.Version {
		t.Fatal("идемпотентная загрузка меняет проекцию")
	}
	next := schema(2)
	next.Categories[0].Attributes[0].Values = []string{"v2"}
	store.FailCommit = errors.New("сбой фиксации")
	if err := service.Load(ctx, next); err == nil {
		t.Fatal("ожидался откат")
	}
	store.FailCommit = nil
	current, err := service.Current(ctx)
	if err != nil || current.Version != 1 {
		t.Fatalf("%+v %v", current, err)
	}
	if err := service.Load(ctx, next); err != nil {
		t.Fatal(err)
	}
	p, _ = store.Product("t1", "p1")
	if p.Card.Publication.Published || p.Card.Publication.Issues[0].Code != "taxonomy.enum_value" {
		t.Fatalf("ошибка должна скрыть карточку: %+v", p)
	}
	if len(store.OutboxRows()) != 1 || !store.Processed("e1") {
		t.Fatal("нет атомарных outbox/inbox")
	}
	if err := service.Load(ctx, schema(1)); !errors.Is(err, taxonomy.ErrConflict) {
		t.Fatal(err)
	}
	if err := service.Load(ctx, schema(2)); !errors.Is(err, taxonomy.ErrConflict) {
		t.Fatal(err)
	}
	// Новая версия восстанавливает карточку из исходного блока без нового события магазина.
	if err := service.Load(ctx, schema(3)); err != nil {
		t.Fatal(err)
	}
	p, _ = store.Product("t1", "p1")
	if !p.Card.Publication.Published || p.Card.Publication.Version != 3 {
		t.Fatalf("%+v", p)
	}
	if err := projector.ApplyProduct(ctx, event); err != nil {
		t.Fatal(err)
	}
	again, _ := store.Product("t1", "p1")
	if again.Version != p.Version {
		t.Fatal("повтор события изменяет проекцию")
	}
}

func TestInvalidCardIsStoredAndNotParked(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	clock := fake.NewClock(time.Now())
	projector := appcatalog.NewProjector(store, clock)
	for i, card := range []*taxonomy.Card{nil, {CategoryID: "missing", Published: true}} {
		event := appcatalog.ProductEvent{EventID: string(rune('a' + i)), Snapshot: catalog.Snapshot{
			TenantID:  "t1",
			ProductID: "p1",
			Revision:  int64(i + 1),
			At:        clock.Now(),
			Card:      catalog.Card{Name: "Товар", Visible: true, Marketplace: card},
		}}
		if err := projector.ApplyProduct(ctx, event); err != nil {
			t.Fatal(err)
		}
		p, _ := store.Product("t1", "p1")
		if p.Card.Publication.Published || len(p.Card.Publication.Issues) != 1 {
			t.Fatalf("%+v", p)
		}
		publication, err := appcatalog.NewQueries(store, fake.NewIndex(), fake.NewPlatform()).Publication(
			ctx,
			ids.OfferID{Tenant: "t1", Product: "p1"},
		)
		if err != nil || publication.Published {
			t.Fatalf("%+v %v", publication, err)
		}
	}
}

func TestRevalidationPagesIncludeNonParticipants(t *testing.T) {
	store := fake.NewStore()
	clock := fake.NewClock(time.Now())
	for i := 0; i < 205; i++ {
		store.SeedProduct(catalog.ProductState{
			TenantID:  "t1",
			ProductID: ids.ProductID(fmt.Sprintf("p%03d", i)),
			Revision:  1,
			Version:   1,
			Card:      catalog.Card{Name: "Товар", Visible: true},
		})
	}
	if err := apptaxonomy.NewService(store, clock).Load(context.Background(), schema(1)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		p, _ := store.Product("t1", ids.ProductID(fmt.Sprintf("p%03d", i)))
		if p.Version != 2 || p.Card.Publication.Version != 1 || p.Card.Publication.Published {
			t.Fatalf("товар пропущен при перепроверке: %+v", p)
		}
	}
	if len(store.OutboxRows()) != 205 {
		t.Fatal(len(store.OutboxRows()))
	}
}
