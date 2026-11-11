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
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestSearchTermsOnlyPublicationPreservesFrozenDictionaries(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	original := schema(11)
	original.Attributes[0].Values = []string{"v1", "v2"}
	original.Attributes[0].ValuesMeta = map[string]taxonomy.ValueMeta{"v1": {Hex: "#FFFFFF"}}
	store.SeedTaxonomy(original)
	seedValues(t, store, "v1", "v2", "v3")
	clock := fake.NewClock(time.Now())
	service := apptaxonomy.NewService(store, clock, nil)
	projector := appcatalog.NewProjector(store, clock, nil)
	err := projector.ApplyProduct(ctx, appcatalog.ProductEvent{
		EventID: "e1",
		Snapshot: catalog.Snapshot{
			TenantID:  "t1",
			ProductID: "p1",
			Revision:  1,
			At:        clock.Now(),
			Card: catalog.Card{
				Name:    "Товар",
				Visible: true,
				Marketplace: &taxonomy.Card{
					CategoryID: "c1",
					Published:  true,
					Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`"v1"`)},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, terms := range []map[string][]string{{"v1": {"первый"}}, {"v1": {"первый"}}, nil} {
		current, err := service.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := store.Product("t1", "p1")
		input := schema(current.Version)
		input.Attributes[0].Values = nil
		input.Attributes[0].ValuesMeta = original.Attributes[0].ValuesMeta
		input.Attributes[0].SearchTermsByValue = terms
		if _, err := service.LoadPart(ctx, "attributes", input, current.Version); err != nil {
			t.Fatal(err)
		}
		next, _ := service.Current(ctx)
		want := original
		want.Version = current.Version + 1
		want.Attributes = append([]taxonomy.Attribute(nil), original.Attributes...)
		want.Attributes[0].SearchTermsByValue = terms
		if !reflect.DeepEqual(want, next) {
			t.Fatalf("метаданные обновили другие части или frozen Values: %+v", next)
		}
		after, _ := store.Product("t1", "p1")
		if !after.Card.Publication.Published || after.Version <= before.Version || len(store.OutboxRows()) == 0 {
			t.Fatalf("публикация не перепроверена: %+v", after)
		}
		if !reflect.DeepEqual(after.Card.Publication.AttributeSearchTerms["a1"], terms["v1"]) {
			t.Fatalf("выражения не обновлены/очищены: %+v", after.Card.Publication)
		}
	}
	current, _ := service.Current(ctx)
	input := schema(current.Version)
	input.Attributes[0].Values = nil
	input.Attributes[0].ValuesMeta = original.Attributes[0].ValuesMeta
	input.Attributes[0].SearchTermsByValue = map[string][]string{"v3": {"третий"}}
	if _, err := service.LoadPart(ctx, "attributes", input, current.Version); !errors.Is(err, taxonomy.ErrInvalid) {
		t.Fatalf("выражение вне frozen Values принято: %v", err)
	}
	next, _ := service.Current(ctx)
	if !reflect.DeepEqual(current, next) {
		t.Fatal("отклонённые метаданные частично опубликованы")
	}
}
