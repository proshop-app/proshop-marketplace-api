//go:build integration

package searchapi_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestFacetValuesSearchContract(t *testing.T) {
	index := testSearchIndex(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	generation, err := index.NewGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	docs := []ports.OfferDocument{
		{
			ID:                   "t1~p1",
			TenantID:             "t1",
			ProductID:            "p1",
			Name:                 "Товар",
			SellerSlug:           "t1",
			SellerName:           "Магазин",
			CategoryID:           "c1",
			CategoryPath:         []string{"c1"},
			Attributes:           map[string]json.RawMessage{"a1": json.RawMessage(`"v1"`)},
			AttributeFacetValues: map[string][]string{"a1": {"v2", "v3"}},
			Visible:              true,
			Listed:               true,
			Version:              1,
			UpdatedAt:            time.Now(),
			Price:                int64(100),
			BasePrice:            int64(100),
		},
	}
	if err := index.PutGeneration(ctx, generation, docs); err != nil {
		t.Fatal(err)
	}
	if err := index.Activate(ctx, generation); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		empty, err := index.NewGeneration(cleanup)
		if err == nil {
			err = index.Activate(cleanup, empty)
		}
		if err != nil {
			t.Error(err)
		}
	})
	for value, count := range map[string]int{"v1": 0, "v2": 1, "v3": 1} {
		result, err := index.Search(ctx, ports.OfferQuery{
			Limit:       10,
			Attributes:  map[string]ports.AttributeFilter{"a1": {Type: "enum", Values: []string{value}}},
			FacetFields: []taxonomy.Attribute{{Code: "a1", Name: "Атрибут", Type: "enum", Facet: true}},
		})
		if err != nil || result.Total != count {
			t.Fatalf("отбор %s: %+v %v", value, result, err)
		}
		for _, doc := range result.Items {
			if string(doc.Attributes["a1"]) != `"v1"` || len(doc.AttributeFacetValues) != 0 {
				t.Fatalf("поиск изменил исходное значение или раскрыл facetValues: %+v", doc)
			}
		}
		found := false
		for _, facet := range result.Facets {
			if facet.Code != "attr.a1" {
				continue
			}
			found = true
			if facet.Count != 1 || len(facet.Values) != 2 {
				t.Fatalf("составное значение посчитано неверно: %+v", facet)
			}
		}
		if !found {
			t.Fatal("фасет характеристики отсутствует")
		}
	}
}
