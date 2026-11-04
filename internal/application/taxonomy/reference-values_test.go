package taxonomy_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestReferenceValuesHydrateSchemaAndPublicationWithoutChangingPGSnapshot(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	store.SeedTaxonomy(schema(1))
	values := &fake.ReferenceValues{Items: map[string][]ports.ReferenceValue{"a1": {
		{ID: "e2", Value: "v3", Order: 2},
		{ID: "e1", Value: "v2", Order: 1, Meta: taxonomy.ValueMeta{Hex: "#ffffff"}},
	}}}
	uow := apptaxonomy.WithReferenceValues(store, values)
	clock := fake.NewClock(time.Now())
	service := apptaxonomy.NewService(uow, clock)
	definition, err := service.Current(ctx)
	if err != nil || !reflect.DeepEqual(definition.Categories[0].Attributes[0].Values, []string{"v2", "v3"}) ||
		definition.Categories[0].Attributes[0].ValuesMeta["v2"].Hex != "#ffffff" {
		t.Fatalf("текущая общая схема: %+v %v", definition, err)
	}
	stored, err := apptaxonomy.NewService(store, clock).Current(ctx)
	if err != nil || !reflect.DeepEqual(stored.Categories[0].Attributes[0].Values, []string{"v1"}) {
		t.Fatalf("чтение подменило PG историю: %+v %v", stored, err)
	}
	event := appcatalog.ProductEvent{EventID: "remote-values", Snapshot: catalog.Snapshot{
		TenantID:  "t1",
		ProductID: "p1",
		Revision:  1,
		At:        clock.Now(),
		Card: catalog.Card{Name: "Товар", Visible: true, Marketplace: &taxonomy.Card{
			CategoryID: "c1",
			Published:  true,
			Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`"v2"`)},
		}},
	}}
	projector := appcatalog.NewProjector(uow, clock)
	if err := projector.ApplyProduct(ctx, event); err != nil {
		t.Fatal(err)
	}
	product, _ := store.Product("t1", "p1")
	if !product.Card.Publication.Published {
		t.Fatalf("подтверждённое Mongo значение не допущено: %+v", product.Card.Publication)
	}
	values.Err = errors.New("владелец недоступен")
	event.EventID = "must-retry"
	event.Snapshot.Revision++
	if err := projector.ApplyProduct(ctx, event); err == nil || store.Processed(event.EventID) {
		t.Fatal("сбой общего реестра подтверждён по старому PG снимку")
	}
}

func TestReferenceValuesApplyUsesOwnerForIncomingAndRevalidation(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	values := &fake.ReferenceValues{Items: map[string][]ports.ReferenceValue{"a1": {{ID: "e1", Value: "v2"}}}}
	service := apptaxonomy.NewService(apptaxonomy.WithReferenceValues(store, values), fake.NewClock(time.Now()))
	if err := service.Load(ctx, schema(1)); err != nil {
		t.Fatal(err)
	}
	current, err := service.Current(ctx)
	if err != nil || !reflect.DeepEqual(current.Categories[0].Attributes[0].Values, []string{"v2"}) {
		t.Fatalf("входящая поставка переписала общий словарь: %+v %v", current, err)
	}
	values.Items = nil
	if _, err := service.Current(ctx); err == nil {
		t.Fatal("пустой ответ заменён устаревшим enum")
	}
}
