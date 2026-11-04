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
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const staleness = 10 * time.Minute

func owner(revision int64) *fake.ReferenceValues {
	return &fake.ReferenceValues{
		Items: map[string][]ports.ReferenceValue{"a1": {{ID: "e1", Value: "v2"}}},
		Dictionaries: []ports.ReferenceRevision{
			{Code: "a1", Kind: "closed", Revision: revision},
			{Code: "brand", Kind: "open", Revision: 7},
		},
	}
}

func publishedEvent(id string, at time.Time) appcatalog.ProductEvent {
	return appcatalog.ProductEvent{EventID: id, Snapshot: catalog.Snapshot{
		TenantID:  "t1",
		ProductID: "p1",
		Revision:  1,
		At:        at,
		Card: catalog.Card{Name: "Товар", Visible: true, Marketplace: &taxonomy.Card{
			CategoryID: "c1",
			Published:  true,
			Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`"v2"`)},
		}},
	}}
}

func TestReferenceCacheServesSchemaWithoutOwnerPerRead(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	store.SeedTaxonomy(schema(1))
	clock := fake.NewClock(time.Now())
	source := owner(1)
	cache := apptaxonomy.NewReferenceCache(source, clock, staleness)
	result, err := cache.Refresh(ctx)
	if err != nil || result.Checked != 1 || !reflect.DeepEqual(result.Updated, []string{"a1"}) {
		t.Fatalf("первая сверка: %+v %v", result, err)
	}
	service := apptaxonomy.NewService(apptaxonomy.WithReferenceValues(store, cache), clock)
	for range 3 {
		definition, err := service.Current(ctx)
		if err != nil || !reflect.DeepEqual(definition.Categories[0].Attributes[0].Values, []string{"v2"}) {
			t.Fatalf("схема из копии: %+v %v", definition, err)
		}
	}
	if source.ValueCalls != 1 {
		t.Fatalf("чтение схемы ходило к владельцу или взяло открытый словарь: %d", source.ValueCalls)
	}

	clock.Advance(time.Minute)
	if result, err := cache.Refresh(ctx); err != nil || len(result.Updated) != 0 || source.ValueCalls != 1 {
		t.Fatalf("словарь той же ревизии перечитан: %+v %v %d", result, err, source.ValueCalls)
	}

	source.Items["a1"] = []ports.ReferenceValue{{ID: "e1", Value: "v2"}, {ID: "e2", Value: "v3", Order: 1}}
	source.Dictionaries[0].Revision = 2
	if result, err := cache.Refresh(ctx); err != nil || !reflect.DeepEqual(result.Updated, []string{"a1"}) {
		t.Fatalf("новая ревизия не перечитана: %+v %v", result, err)
	}
	definition, err := service.Current(ctx)
	if err != nil || !reflect.DeepEqual(definition.Categories[0].Attributes[0].Values, []string{"v2", "v3"}) {
		t.Fatalf("схема после новой ревизии: %+v %v", definition, err)
	}
}

func TestReferenceCacheKeepsLastCopyWhileOwnerIsDown(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	store.SeedTaxonomy(schema(1))
	clock := fake.NewClock(time.Now())
	source := owner(1)
	cache := apptaxonomy.NewReferenceCache(source, clock, staleness)
	if _, err := cache.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	source.Err = errors.New("владелец недоступен")
	clock.Advance(9 * time.Minute)
	if _, err := cache.Refresh(ctx); err == nil {
		t.Fatal("сбой владельца не дошёл до цикла")
	}
	if values, err := cache.Values(ctx, "a1"); err != nil || len(values) != 1 {
		t.Fatalf("свежая копия не отдана при сбое владельца: %+v %v", values, err)
	}

	clock.Advance(2 * time.Minute)
	result, err := cache.Refresh(ctx)
	if err == nil || result.Age < 11*time.Minute {
		t.Fatalf("давность копии: %+v %v", result, err)
	}
	if _, err := cache.Values(ctx, "a1"); errs.CodeOf(err) != errs.Unavailable {
		t.Fatalf("давняя копия допущена к проверке публикации: %v", err)
	}
	projector := appcatalog.NewProjector(apptaxonomy.WithReferenceValues(store, cache), clock)
	event := publishedEvent("stale", clock.Now())
	if err := projector.ApplyProduct(ctx, event); err == nil || store.Processed(event.EventID) {
		t.Fatal("публикация подтверждена по давней копии")
	}
	if values, err := cache.Lasting().Values(ctx, "a1"); err != nil || len(values) != 1 {
		t.Fatalf("выдача покупателю упала вместе с владельцем: %+v %v", values, err)
	}

	source.Err = nil
	calls := source.ValueCalls
	if result, err := cache.Refresh(ctx); err != nil || len(result.Updated) != 0 || result.Age != 0 {
		t.Fatalf("восстановление владельца: %+v %v", result, err)
	}
	if err := projector.ApplyProduct(ctx, event); err != nil || source.ValueCalls != calls {
		t.Fatalf("после сверки той же ревизии: %v, обращений %d", err, source.ValueCalls-calls)
	}
}

func TestReferenceCacheLoadsColdDictionaryOnceAndDropsRemoved(t *testing.T) {
	ctx := context.Background()
	clock := fake.NewClock(time.Now())
	source := owner(3)
	cache := apptaxonomy.NewReferenceCache(source, clock, staleness)
	for range 2 {
		if values, err := cache.Values(ctx, "a1"); err != nil || len(values) != 1 {
			t.Fatalf("первое чтение до сверки: %+v %v", values, err)
		}
	}
	if source.ValueCalls != 1 {
		t.Fatalf("холодный словарь читался %d раз", source.ValueCalls)
	}
	if result, err := cache.Refresh(ctx); err != nil || !reflect.DeepEqual(result.Updated, []string{"a1"}) {
		t.Fatalf("ревизия холодного словаря не запомнена: %+v %v", result, err)
	}
	if result, err := cache.Refresh(ctx); err != nil || len(result.Updated) != 0 {
		t.Fatalf("словарь перечитан без новой ревизии: %+v %v", result, err)
	}

	if _, err := cache.Values(ctx, "a2"); err == nil {
		t.Fatal("пустой словарь владельца принят")
	}
	if _, err := cache.Lasting().Values(ctx, "a2"); err == nil {
		t.Fatal("пустой словарь запомнен")
	}

	source.Dictionaries = []ports.ReferenceRevision{}
	if result, err := cache.Refresh(ctx); err != nil || !reflect.DeepEqual(result.Dropped, []string{"a1"}) {
		t.Fatalf("удалённый у владельца словарь остался: %+v %v", result, err)
	}
	source.Err = errors.New("владелец недоступен")
	if _, err := cache.Lasting().Values(ctx, "a1"); err == nil {
		t.Fatal("удалённый словарь отдан из копии")
	}
}

func TestDuplicateProductEventSkipsReferenceValues(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	store.SeedTaxonomy(schema(1))
	clock := fake.NewClock(time.Now())
	values := owner(1)
	projector := appcatalog.NewProjector(apptaxonomy.WithReferenceValues(store, values), clock)
	event := publishedEvent("once", clock.Now())
	if err := projector.ApplyProduct(ctx, event); err != nil {
		t.Fatal(err)
	}
	values.Err = errors.New("владелец недоступен")
	calls := values.ValueCalls
	if err := projector.ApplyProduct(ctx, event); err != nil || values.ValueCalls != calls {
		t.Fatalf("повтор события читал схему: %v, обращений %d", err, values.ValueCalls-calls)
	}
}
