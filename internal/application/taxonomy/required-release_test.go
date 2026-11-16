package taxonomy_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestReleaseRequiresSellerEnumAndHidesMissingValue(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	seedValues(t, store, "v1")
	clock := fake.NewClock(time.Now())
	service := apptaxonomy.NewService(store, clock, nil)
	if err := applyDefinition(ctx, service, schema(1)); err != nil {
		t.Fatal(err)
	}
	projector := appcatalog.NewProjector(store, clock, nil)
	event := appcatalog.ProductEvent{
		EventID: "e1",
		Snapshot: catalog.Snapshot{
			TenantID:  "t1",
			ProductID: "p1",
			Revision:  1,
			At:        clock.Now(),
			Card: catalog.Card{
				Name:        "Товар",
				Visible:     true,
				Marketplace: &taxonomy.Card{CategoryID: "c1", Published: true},
			},
		},
	}
	if err := projector.ApplyProduct(ctx, event); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Product("t1", "p1")
	if !before.Card.Publication.Published {
		t.Fatal("исходная карточка не опубликована")
	}
	next := schema(2)
	next.Schemas["s1"] = taxonomy.Schema{Attributes: []string{"a1"}}
	if err := applyDefinition(ctx, service, next); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Product("t1", "p1")
	if after.Card.Publication.Published || len(after.Card.Publication.Issues) == 0 {
		t.Fatalf("карточка без обязательного поля не скрыта: %+v", after.Card.Publication)
	}
	if after.Revision != before.Revision || after.Version <= before.Version || len(store.OutboxRows()) == 0 {
		t.Fatal("перепроверка должна обновить проекцию и outbox без события продавца")
	}
	event.EventID = "e2"
	event.Snapshot.Revision = 2
	event.Snapshot.Card.Marketplace.Attributes = map[string]json.RawMessage{"a1": json.RawMessage(`"v1"`)}
	if err := projector.ApplyProduct(ctx, event); err != nil {
		t.Fatal(err)
	}
	after, _ = store.Product("t1", "p1")
	if !after.Card.Publication.Published || string(after.Card.Publication.Attributes["a1"]) != `"v1"` {
		t.Fatalf("заполнение продавцом не восстановило публикацию: %+v", after.Card.Publication)
	}
}
