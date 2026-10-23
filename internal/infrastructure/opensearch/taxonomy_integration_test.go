//go:build integration

package opensearch_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestDynamicCanonicalFieldsAndAggregations(t *testing.T) {
	index := testIndex(t)
	activate(t, index)
	ctx := context.Background()
	d1 := offer("t1~p1", "Товар 1", 100, 1)
	d1.CategoryID = "c2"
	d1.CategoryPath = []string{"c1", "c2"}
	d1.Attributes = map[string]json.RawMessage{
		"a1": json.RawMessage(`"v1"`),
		"a2": json.RawMessage(`1`),
		"a3": json.RawMessage(`false`),
		"a4": json.RawMessage(`"Текст"`),
	}
	if err := index.Put(ctx, []ports.OfferDocument{d1}); err != nil {
		t.Fatal(err)
	}
	// Первое целое значение не должно закрепить long и обрезать следующую дробь.
	d2 := offer("t2~p1", "Товар 2", 200, 1)
	d2.Attributes = map[string]json.RawMessage{
		"a1": json.RawMessage(`"v2"`),
		"a2": json.RawMessage(`1.75`),
		"a3": json.RawMessage(`true`),
	}
	if err := index.Put(ctx, []ports.OfferDocument{d2}); err != nil {
		t.Fatal(err)
	}
	if err := index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	fields := []taxonomy.Attribute{
		{Code: "a1", Name: "Атрибут 1", Type: "enum", Facet: true},
		{Code: "a2", Name: "Атрибут 2", Type: "number", Facet: true},
		{Code: "a3", Name: "Атрибут 3", Type: "boolean", Facet: true},
		{Code: "a4", Name: "Атрибут 4", Type: "string", Facet: true},
	}
	page, err := index.Search(ctx, ports.OfferQuery{Limit: 1, FacetFields: fields})
	if err != nil || page.Total != 2 || len(page.Facets) != 8 {
		t.Fatalf("%+v %v", page, err)
	}
	byCode := map[string]ports.Facet{}
	for _, f := range page.Facets {
		byCode[f.Code] = f
	}
	if byCode["attr.a2"].Min.String() != "1.0" && byCode["attr.a2"].Min.String() != "1" {
		t.Fatal(byCode)
	}
	if byCode["attr.a2"].Max.String() != "1.75" || byCode["attr.a4"].Count != 1 {
		t.Fatal(byCode)
	}
	if len(byCode["attr.a3"].Values) != 2 {
		t.Fatalf("boolean keyword: %+v", byCode["attr.a3"])
	}
	page, err = index.Search(ctx, ports.OfferQuery{Limit: 10, Attributes: map[string]ports.AttributeFilter{
		"a2": {Min: "1.5", Max: "2"},
		"a1": {Values: []string{"v1", "v2"}},
	}})
	if err != nil || page.Total != 1 || page.Items[0].ID != d2.ID {
		t.Fatalf("%+v %v", page, err)
	}
	page, err = index.Search(ctx, ports.OfferQuery{Limit: 10, CategoryID: "c2"})
	if err != nil || page.Total != 1 || page.Items[0].ID != d1.ID {
		t.Fatalf("%+v %v", page, err)
	}
	page, err = index.Search(ctx, ports.OfferQuery{Limit: 10, Attributes: map[string]ports.AttributeFilter{
		"a3": {Values: []string{"false"}},
		"a4": {Values: []string{"Текст"}},
	}})
	if err != nil || page.Total != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	// Устаревший документ не возвращает атрибуты после скрытия новой версией.
	if err := index.Put(ctx, []ports.OfferDocument{{
		ID:        d1.ID,
		TenantID:  d1.TenantID,
		ProductID: d1.ProductID,
		Version:   2,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := index.Put(ctx, []ports.OfferDocument{d1}); err != nil {
		t.Fatal(err)
	}
	if err := index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	page, err = index.Search(ctx, ports.OfferQuery{Limit: 10})
	if err != nil || page.Total != 1 {
		t.Fatalf("%+v %v", page, err)
	}
}
