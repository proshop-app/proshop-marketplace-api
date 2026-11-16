package catalog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/colorpalette"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestColorRegistryPublicationDocumentAndSearch(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	clock := fake.NewClock(time.Now())
	registry := appreference.NewService(store, clock, &fake.IDs{}, &fake.Publisher{}, appreference.Config{})
	store.SeedReferenceDictionary(reference.DictionaryState{
		Code:       "color",
		Kind:       reference.KindClosed,
		AliasKinds: reference.AliasKinds,
		Limits:     reference.DefaultLimits(),
	})
	field := taxonomy.Attribute{
		Code:       "color",
		Name:       "Цвет",
		Type:       "enum",
		Dictionary: "color",
		Facet:      true,
		ValuesMeta: map[string]taxonomy.ValueMeta{colorpalette.Other: {Swatch: "other"}},
	}
	create := func(name string, status reference.Status, metadata string) appreference.EntryResult {
		t.Helper()
		result, err := registry.Create(ctx, appreference.CreateCommand{
			Dictionary: "color",
			Name:       name,
			Status:     status,
			Metadata:   json.RawMessage(metadata),
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, value := range colorpalette.Values() {
		create(value, reference.StatusConfirmed, `{}`)
		field.ValuesMeta[value] = taxonomy.ValueMeta{Hex: "#FFFFFF"}
	}
	shades := []string{"Хаки", "Оливковый", "Мятный", "Лайм", "Сине-зелёный"}
	for _, shade := range shades {
		metadata := `{"baseColors":["Зелёный"]}`
		if shade == "Сине-зелёный" {
			metadata = `{"baseColors":["Синий","Зелёный"]}`
		}
		result := create(shade, reference.StatusConfirmed, metadata)
		if shade == "Хаки" {
			if _, err := registry.AddAlias(ctx, appreference.AddAliasCommand{
				Dictionary: "color",
				EntryID:    result.Entry.ID,
				Alias:      appreference.AliasSpec{Text: "Вариант"},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	candidate := create("Кандидат", reference.StatusCandidate, `{}`)
	legacy := create("Оттенок", reference.StatusCandidate, `{}`)
	// Имитируем состояние, подтверждённое до введения обязательной привязки.
	err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		entries, err := tx.Reference().Entries(ctx, "color", []ids.UUID{legacy.Entry.ID})
		if err != nil {
			return err
		}
		entry := entries[0]
		if _, err := entry.ChangeStatus(reference.StatusConfirmed, clock.Now()); err != nil {
			return err
		}
		entry.Bump(clock.Now())
		return tx.Reference().Save(ctx, []*reference.Entry{entry}, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := taxonomy.Definition{
		Version:    1,
		Attributes: []taxonomy.Attribute{field},
		Schemas:    map[string]taxonomy.Schema{"s1": {Attributes: []string{"color"}}},
		Categories: []taxonomy.Category{{ID: "c1", Slug: "c1", Name: "Категория", Schema: "s1"}},
	}
	seedSeller(store, "t1", participant.TenantActive, true)
	store.SeedTaxonomy(definition)
	service := apptaxonomy.NewService(store, clock, nil)
	input := definition
	input.Attributes = append([]taxonomy.Attribute(nil), definition.Attributes...)
	input.Attributes[0].Values = []string{"подмена"}
	input.Attributes[0].FacetOptions = []string{"подмена"}
	input.Attributes[0].FacetValuesByValue = map[string][]string{"подмена": {"подмена"}}
	input.Attributes[0].ValuesMeta = maps.Clone(field.ValuesMeta)
	delete(input.Attributes[0].ValuesMeta, colorpalette.Other)
	_, publicationErr := service.LoadPart(ctx, "attributes", input, 1)
	if typed, ok := errs.As(publicationErr); !ok || typed.Key() != "taxonomy.invalid" ||
		typed.Message() != "нет образца значения фасета: Другие цвета" {
		t.Fatalf("отказ публикации потерял контекст или причину: %v", publicationErr)
	}
	input.Attributes[0].ValuesMeta = field.ValuesMeta
	if _, err := service.LoadPart(ctx, "attributes", input, 1); err != nil {
		t.Fatal(err)
	}
	published, _ := service.Current(ctx)
	for _, value := range []string{"Произвольный", "Вариант", candidate.Entry.CanonicalName, colorpalette.Other} {
		raw, _ := json.Marshal(value)
		result := published.Check(&taxonomy.Card{
			CategoryID: "c1",
			Published:  true,
			Attributes: map[string]json.RawMessage{"color": raw},
		})
		if result.Published || len(result.Issues) == 0 || result.Issues[0].Code != "taxonomy.enum_value" {
			t.Fatalf("незакрытый enum: %s %+v", value, result)
		}
	}
	index := fake.NewIndex()
	projector := appcatalog.NewProjector(store, clock, nil)
	put := func(product ids.ProductID, shade string) {
		t.Helper()
		seedProduct(store, "t1", product)
		event := snapshot("t1", product, 2, clock.Now())
		raw, _ := json.Marshal(shade)
		event.Card.Marketplace = &taxonomy.Card{
			CategoryID: "c1",
			Published:  true,
			Attributes: map[string]json.RawMessage{"color": raw},
		}
		if err := projector.ApplyProduct(ctx, productEvent(string(product), event)); err != nil {
			t.Fatal(err)
		}
		if err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			state, err := tx.Offers().Offer(ctx, "t1", product)
			if err != nil {
				return err
			}
			doc, ok := appcatalog.Document(state)
			if !ok || !doc.Listed || string(doc.Attributes["color"]) != string(raw) {
				t.Fatalf("неверная проекция: %+v", doc)
			}
			return index.Put(ctx, []ports.OfferDocument{doc})
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i, shade := range shades {
		put(ids.ProductID(fmt.Sprintf("p%d", i)), shade)
	}
	queries := appcatalog.NewQueries(store, index, fake.NewPlatform(), nil, nil, clock)
	query := ports.OfferQuery{Attributes: map[string]ports.AttributeFilter{"color": {Values: []string{"Зелёный"}}}}
	page, err := queries.Search(ctx, query)
	if err != nil || page.Total != 5 {
		t.Fatalf("зелёный не нашёл пять оттенков: %+v %v", page, err)
	}
	want := []ports.FacetBucket{{Value: "Зелёный", Count: 5}, {Value: "Синий", Count: 1}}
	if got := findFacet(t, page.Facets, "attr.color"); !reflect.DeepEqual(got.Values, want) || got.Count != 5 {
		t.Fatalf("неверный фасет: %+v", got)
	}
	put("p5", "Оттенок")
	page, err = queries.Search(ctx, ports.OfferQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, ports.FacetBucket{Value: colorpalette.Other, Count: 1})
	if got := findFacet(t, page.Facets, "attr.color"); !reflect.DeepEqual(got.Values, want) {
		t.Fatalf("корзина не последняя: %+v", got)
	}
	page, err = queries.Search(ctx, ports.OfferQuery{
		Attributes: map[string]ports.AttributeFilter{"color": {Values: []string{colorpalette.Other}}},
	})
	if err != nil || page.Total != 1 {
		t.Fatalf("отбор по корзине: %+v %v", page, err)
	}
	beforeTasks := len(store.OutboxRows())
	_, err = registry.Update(ctx, appreference.UpdateCommand{
		Dictionary: "color",
		ID:         legacy.Entry.ID,
		IfMatch:    legacy.Entry.Revision,
		Metadata:   json.RawMessage(`{"baseColors":["Зелёный"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen, _ := service.Current(ctx)
	if !reflect.DeepEqual(published, frozen) || len(store.OutboxRows()) != beforeTasks {
		t.Fatal("правка метаданных изменила снимок или поставила задачу")
	}
	if _, err := service.LoadPart(ctx, "schemas", definition, frozen.Version); err != nil {
		t.Fatal(err)
	}
	product, _ := store.Product("t1", "p5")
	if !reflect.DeepEqual(product.Card.Publication.AttributeFacetValues["color"], []string{"Зелёный"}) {
		t.Fatal("публикация не перепроверила товар")
	}
}
