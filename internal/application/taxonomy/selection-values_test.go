package taxonomy_test

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestSelectionValuesStayScopedAndFollowConfirmation(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	clock := fake.NewClock(time.Now())
	registry := appreference.NewService(store, clock, &fake.IDs{}, &fake.Publisher{}, appreference.Config{})
	pools := reference.ImportPackage{
		SchemaVersion: 1,
		Sources:       []reference.ImportSource{{ID: "s1", URL: "https://example.org/source", License: "CC0-1.0"}},
		Dictionaries: []reference.ImportDictionary{
			{
				Code:    "d1",
				Kind:    reference.KindOpen,
				Release: 1,
				Entries: []reference.DeliveryEntry{
					{
						Canonical: "46",
						Status:    reference.StatusConfirmed,
						Metadata:  json.RawMessage(`{"secondaryLabel":"L"}`),
					},
				},
			},
			{
				Code:    "d2",
				Kind:    reference.KindOpen,
				Release: 1,
				Entries: []reference.DeliveryEntry{
					{Canonical: "44", Status: reference.StatusConfirmed},
					{
						Canonical: "46",
						Status:    reference.StatusConfirmed,
						Metadata:  json.RawMessage(`{"secondaryLabel":"XL","order":3}`),
						Aliases:   []reference.DeliveryAlias{{Text: "46 RU"}},
					},
					{
						Canonical: "44–46",
						Status:    reference.StatusCandidate,
						Metadata:  json.RawMessage(`{"covers":["44","46"]}`),
						Aliases:   []reference.DeliveryAlias{{Text: "44/46"}},
					},
				},
			},
		},
	}
	results, err := registry.Import(ctx, "", pools)
	if err != nil {
		t.Fatal(err)
	}
	service := apptaxonomy.NewService(store, clock, nil)
	release := taxonomy.Release{
		Version:  16,
		Checksum: strings.Repeat("a", 64),
		Attributes: []taxonomy.Attribute{
			{
				Code:                "a1",
				Type:                "string",
				Name:                "Точное имя",
				DisplayName:         "Имя",
				Dictionary:          "d1",
				DictionarySelection: true,
			},
		},
		Schemas: map[string]taxonomy.Schema{"s1": {Attributes: []string{"a1"}}},
		Categories: []taxonomy.Category{
			{ID: "c1", Schema: "s1", Name: "Раздел 1", Slug: "c1"},
			{ID: "c2", Schema: "s1", Name: "Раздел 2", Slug: "c2"},
		},
		DictionaryBindings: []taxonomy.DictionaryBinding{
			{CategoryID: "c2", AttributeCode: "a1", Mode: taxonomy.BindingReplace, Dictionaries: []string{"d2"}},
		},
	}
	if _, err := service.Apply(ctx, release); err != nil {
		t.Fatal(err)
	}
	before, _ := service.Current(ctx)
	c1, _, _ := before.Category("c1")
	c2, _, _ := before.Category("c2")
	if c1.Attributes[0].ValuesMeta["46"].SecondaryLabel != "L" ||
		c2.Attributes[0].ValuesMeta["46"].SecondaryLabel != "XL" {
		t.Fatal("метаданные сеток смешаны")
	}
	card := taxonomy.Card{
		CategoryID: "c2",
		Published:  true,
		Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`"46"`)},
	}
	got := before.Check(&card)
	if !got.Published || !slices.Equal(got.AttributeSearchTerms["a1"], []string{"XL", "46 RU"}) {
		t.Fatalf("публикация подтверждённого значения: %+v", got)
	}
	card.Attributes["a1"] = json.RawMessage(`"44–46"`)
	for _, value := range []string{`"44–46"`, `"44/46"`, `"L"`} {
		card.Attributes["a1"] = json.RawMessage(value)
		rejected := before.Check(&card)
		if rejected.Published || len(rejected.Issues) != 1 ||
			rejected.Issues[0].Code != "taxonomy.dictionary_value" || rejected.Issues[0].Message == "" {
			t.Fatalf("неподтверждённая строка %s принята: %+v", value, rejected)
		}
	}

	refresh := service.NewSelectionRefresh()
	if err := refresh.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Review(ctx, appreference.ReviewCommand{
		Dictionary: "d2",
		IfMatch:    results[1].Revision,
		Status:     reference.StatusConfirmed,
		Selection: reference.ReviewSelection{
			Mode:        reference.ReviewAllCandidates,
			ExcludedIDs: []ids.UUID{},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := refresh.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := service.Current(ctx)
	card.Attributes["a1"] = json.RawMessage(`"44–46"`)
	published := after.Checker().Check(&card)
	if after.Version != before.Version+1 || !published.Published ||
		string(published.Attributes["a1"]) != `"44–46"` ||
		!slices.Equal(published.AttributeFacetValues["a1"], []string{"44", "46"}) ||
		!slices.Equal(published.AttributeSearchTerms["a1"], []string{"44/46"}) {
		t.Fatalf("диапазон потерян после подтверждения: версия %d, %+v", after.Version, published)
	}
	if !reflect.DeepEqual(taxonomy.AffectedCategories(before, after), []string{"c2"}) {
		t.Fatal("изменение одной сетки затрагивает другие разделы")
	}
	if err := refresh.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	current, _ := service.Current(ctx)
	if current.Version != after.Version {
		t.Fatal("неизменившийся справочник опубликовал новую версию")
	}
}
