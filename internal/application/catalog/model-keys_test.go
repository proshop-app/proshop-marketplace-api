package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

type modelResolver struct {
	id    string
	err   error
	calls int
}

type modelObserver struct{ failures int }

func (r *modelResolver) Resolve(context.Context, string) (string, bool, error) {
	r.calls++
	return r.id, r.id != "", r.err
}

func (r *modelResolver) ResolveDictionary(ctx context.Context, dictionary, value string) (string, bool, error) {
	if dictionary == "brand" {
		return r.Resolve(ctx, value)
	}
	return "entry-" + value, true, nil
}

func (o *modelObserver) Conflict() {}

func (o *modelObserver) ResolveFailed() {
	o.failures++
}

func identitySchema(name string, model, axes, exact []string) taxonomy.CategorySchema {
	return taxonomy.CategorySchema{
		Category: taxonomy.Category{Schema: name},
		Identity: &taxonomy.SchemaIdentity{Model: model, Axes: axes, Exact: exact},
		Attributes: []taxonomy.SchemaAttribute{
			{Attribute: taxonomy.Attribute{Code: "brand", Type: "string", Dictionary: "brand"}},
			{Attribute: taxonomy.Attribute{Code: "model", Type: "string"}},
			{Attribute: taxonomy.Attribute{Code: "color", Type: "enum", Dictionary: "color"}},
			{Attribute: taxonomy.Attribute{Code: "storage_capacity", Type: "number"}},
			{Attribute: taxonomy.Attribute{Code: "size_shoes_eu", Type: "string"}},
			{Attribute: taxonomy.Attribute{Code: "book_isbn", Type: "string", Format: "isbn"}},
			{Attribute: taxonomy.Attribute{Code: "gtin", Type: "string", Format: "gtin"}},
		},
	}
}

func TestSchemaIdentityKeys(t *testing.T) {
	service := NewModelKeys(&modelResolver{id: "brand-1"}, nil, true)
	attrs := map[string]json.RawMessage{
		"brand":            json.RawMessage(`"Some Brand"`),
		"model":            json.RawMessage(`" AB-12 "`),
		"color":            json.RawMessage(`"black"`),
		"storage_capacity": json.RawMessage(`128.0`),
		"size_shoes_eu":    json.RawMessage(`"42"`),
	}
	tests := []struct {
		name    string
		axes    []string
		variant string
	}{
		{name: "phone", axes: []string{"color", "storage_capacity"}, variant: "entry-black:128"},
		{name: "shoe", axes: []string{"color", "size_shoes_eu"}, variant: "entry-black:42"},
		{name: "tablet", axes: []string{"color", "storage_capacity"}, variant: "entry-black:128"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema := identitySchema(test.name, []string{"brand", "model"}, test.axes, []string{"gtin"})
			model, variant := service.calculate(context.Background(), schema, attrs)
			if model != test.name+":brand-1:ab12" || variant != model+":"+test.variant {
				t.Fatal(model, variant)
			}
			attrs["gtin"] = json.RawMessage(`"036000291452"`)
			_, variant = service.calculate(context.Background(), schema, attrs)
			if variant != "exact:0036000291452" {
				t.Fatal(variant)
			}
			delete(attrs, "gtin")
		})
	}
	schema := identitySchema("book", []string{"book_isbn"}, nil, nil)
	for _, isbn := range []string{`"0-306-40615-2"`, `"9780306406157"`} {
		attrs["book_isbn"] = json.RawMessage(isbn)
		model, variant := service.calculate(context.Background(), schema, attrs)
		if model != "book:9780306406157" || variant != model {
			t.Fatal(model, variant)
		}
	}
}

func TestIdentityMissingAndResolverFailure(t *testing.T) {
	resolver := &modelResolver{id: "brand-1"}
	observer := &modelObserver{}
	service := NewModelKeys(resolver, observer, true)
	schema := identitySchema("s1", []string{"brand", "model"}, []string{"color"}, []string{"gtin"})
	attrs := map[string]json.RawMessage{
		"brand": json.RawMessage(`"Brand"`),
		"model": json.RawMessage(`"A1"`),
		"color": json.RawMessage(`"black"`),
		"gtin":  json.RawMessage(`"036000291452"`),
	}
	for _, code := range []string{"brand", "model", "color"} {
		original := attrs[code]
		for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`" "`)} {
			attrs[code] = raw
			model, variant := service.calculate(context.Background(), schema, attrs)
			if model != "" || variant != "" {
				t.Fatal(code, model, variant)
			}
		}
		attrs[code] = original
	}
	resolver.err = errors.New("registry unavailable")
	model, variant := service.calculate(context.Background(), schema, attrs)
	if model != "" || variant != "" || observer.failures != 1 {
		t.Fatal(model, variant, observer.failures)
	}
	product := catalog.RestoreProduct(catalog.ProductState{
		Version: 3,
		Card:    catalog.Card{ModelKey: "old", ModelKeySource: "identity", VariantKey: "old-variant"},
	})
	changed := NewModelKeys(resolver, observer, false).Apply(context.Background(), product, taxonomy.Definition{})
	if !changed || product.State().Card.ModelKey != "" || product.State().Card.VariantKey != "" {
		t.Fatal("отключение должно очищать оба ключа")
	}
}

func TestApplyIdentityRecalculatesAndClearsKeys(t *testing.T) {
	resolver := &modelResolver{id: "brand-1"}
	service := NewModelKeys(resolver, nil, true)
	schema := identitySchema("s1", []string{"brand", "model"}, nil, nil)
	definition := taxonomy.Definition{
		Categories: []taxonomy.Category{{ID: "c1", Schema: "s1"}},
		Schemas: map[string]taxonomy.Schema{
			"s1": {Attributes: []string{"brand", "model"}, Identity: schema.Identity},
		},
	}
	for _, field := range schema.Attributes {
		definition.Attributes = append(definition.Attributes, field.Attribute)
	}
	product := catalog.RestoreProduct(catalog.ProductState{
		Version: 3,
		Card: catalog.Card{
			Publication: taxonomy.Publication{
				CategoryID: "c1",
				Attributes: map[string]json.RawMessage{
					"brand": json.RawMessage(`"Brand"`),
					"model": json.RawMessage(`" Модель-1 "`),
				},
			},
		},
	})
	if !service.Apply(context.Background(), product, definition) {
		t.Fatal("ключи не записаны")
	}
	state := product.State()
	if state.Card.ModelKey != "s1:brand-1:модель1" || state.Card.VariantKey != state.Card.ModelKey ||
		state.Card.ModelKeySource != "identity" || state.Version != 4 {
		t.Fatal(state)
	}
	if service.Apply(context.Background(), product, definition) {
		t.Fatal("повтор изменил версию")
	}
	resolver.id = "brand-2"
	service.Apply(context.Background(), product, definition)
	if product.State().Card.ModelKey != "s1:brand-2:модель1" {
		t.Fatal("слияние не пересчитало ключ")
	}
	resolver.id = ""
	service.Apply(context.Background(), product, definition)
	if product.State().Card.ModelKey != "" || product.State().Card.VariantKey != "" {
		t.Fatal("неизвестный бренд не очистил ключи")
	}
}

func TestIdentityFormatDoesNotDependOnAttributeCode(t *testing.T) {
	service := NewModelKeys(nil, nil, true)
	for _, test := range []struct {
		code     string
		format   string
		input    string
		expected string
	}{
		{code: "book_isbn", format: "isbn", input: `"0-306-40615-2"`, expected: "9780306406157"},
		{code: "gtin", format: "gtin", input: `"036000291452"`, expected: "0036000291452"},
	} {
		t.Run(test.format, func(t *testing.T) {
			for _, code := range []string{test.code, "identifier"} {
				schema := taxonomy.CategorySchema{
					Category: taxonomy.Category{Schema: "s1"},
					Identity: &taxonomy.SchemaIdentity{Model: []string{code}},
					Attributes: []taxonomy.SchemaAttribute{
						{Attribute: taxonomy.Attribute{Code: code, Type: "string", Format: test.format}},
					},
				}
				attributes := map[string]json.RawMessage{code: json.RawMessage(test.input)}
				model, variant := service.calculate(context.Background(), schema, attributes)
				if model != "s1:"+test.expected || variant != model {
					t.Fatal(code, model, variant)
				}
				schema.Attributes[0].Format = ""
				plainModel, _ := service.calculate(context.Background(), schema, attributes)
				if plainModel == model {
					t.Fatal("правило формата применилось без format", code, plainModel)
				}
			}
		})
	}
}
