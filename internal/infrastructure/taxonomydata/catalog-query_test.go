package taxonomydata

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// Байты PrepareQuery и текст ошибок зафиксированы для ранжирования фасетов по охвату встроенной поставки.
// Проверяем все поля FacetFields, порядок, типы фильтров, nil/пустые значения и остальные поля запроса.
var bundledQueryDigests = map[string]string{
	"":                                   "5aac6fbc2a963fbdc529dfed7568557a23bafe4e2ef0729d1bf04368da1e504a",
	"electronics":                        "c4e8fdbb19886f7c11546d973c016f69710a7cae69eb0fa3653ad48b17b21097",
	"electronics.family-mobile":          "78fffb73f72f593758e9e3b686054cf552c93f0779a77d4b972dff91af716d51",
	"electronics.smartphones":            "58e43bf2f7af3854e451778436311e1c36040301050f09460dc0a628ba972ff3",
	"clothing":                           "ea3406013891b31a4c9eadb92c8631ccfbab7777a787ad92ee55d60389636f2a",
	"clothing.women":                     "98a7ac3dd5a915169d08ae05e395f65516e118438d37e9884fccd12c27be1fc3",
	"clothing.women-family-underwear":    "1afa5227869e1c08ab5ab0222d4556caaba72a765c6af0a0ffb808d4a75a5713",
	"clothing.bras":                      "b7f172dc9b61d982bfaed22fc1c3e2e38d30a3e0c76a71f738a0be1935502e33",
	"auto":                               "966969e2e06f2d9f1f59af243db2187ee506510eb358bd148e3969f80625c7db",
	"auto.family-parts":                  "a90be42b5316322a8ff11350d6469295d6ec5cffa66cdcc96f86b6387002e127",
	"auto.family-parts-braking":          "4402c074c53694f808a825fd877d7efbdda0bcf45188b4c1c193857d288a9187",
	"auto.brake-pads":                    "a098847d78b52b54b0bad132cee03d05b83045487e7e778fc77517af19986c44",
	"pets":                               "270fddb6a206a51a71a6de099db13af489062bb958487bf072567b78c9ceae83",
	"pets.family-food":                   "ba6a5665d73d7e49b94ccef358965935a542a6e23c2daa786bad0925cb1d8560",
	"pets.pet-treats":                    "36dbade10c814274433282bbe8047490ebc06bf25dd84c57794c86d8d2aeca3e",
	"industry":                           "cfa4bb2bb3bf9f89376f835501eae1b267562da4ddcf377d0f6ac52dbdcb8649",
	"industry.family-equipment":          "3b424c096b83a35d7327dbd806426f1a5c2210ff3eab3b95a36805ebba433160",
	"industry.family-equipment-machines": "054bd7115c83045404d42c61214cd6a3aa4d6f74e2273e96b7f65a6f53843320",
	"industry.soldering-stations":        "8ad8e8af7fccd7079ba9c11d7fc78fc634af750a405c3d3eeafb0faec788cba5",
	"food":                               "14bd93607f008dd7f2eeca5b6b8ecaf627154e956a93476f5febc55ed6515f6e",
	"food.grains":                        "7518b29bcd4c1e0bdfc61b66adbb8f960009ec1914f90b110e9d632dae25aec9",
}

func TestBundledCatalogPrepareQuery(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	store := fake.NewStore()
	store.SeedTaxonomy(release.Definition())
	queries := appcatalog.NewQueries(
		store,
		fake.NewIndex(),
		nil,
		nil,
		nil,
		fake.NewClock(time.Time{}),
	)
	// По одному фильтру: ошибка не зависит от порядка обхода map.
	filters := []map[string]ports.AttributeFilter{
		nil,
		{"brand": {Values: []string{"v1"}}},
		{"brand": {Values: []string{""}}},
		{"brand": {Min: "1"}},
		{"length": {Min: "1", Max: "2"}},
		{"length": {Min: "2", Max: "1"}},
		{"length": {Values: []string{"1"}}},
		{"length": {Values: []string{"v1"}}},
		{"wireless": {Values: []string{"true"}}},
		{"wireless": {Values: []string{"v1"}}},
		{"color": {}},
		{"color": {Values: []string{"v1"}}},
		{"purpose": {Values: []string{"v1"}}},
		{"gender": {}},
		{"unknown_field": {}},
	}
	for id, want := range bundledQueryDigests {
		t.Run(id, func(t *testing.T) {
			digest := sha256.New()
			encoder := json.NewEncoder(digest)
			for _, filter := range filters {
				// PrepareQuery добавляет Type в map: каждый вызов получает собственную копию.
				var attributes map[string]ports.AttributeFilter
				if filter != nil {
					attributes = map[string]ports.AttributeFilter{}
					for code, value := range filter {
						attributes[code] = value
					}
				}
				prepared, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{
					CategoryID: id,
					Attributes: attributes,
				})
				if err := encoder.Encode(prepared); err != nil {
					t.Fatal(err)
				}
				if err := encoder.Encode(fmt.Sprint(err)); err != nil {
					t.Fatal(err)
				}
			}
			got := fmt.Sprintf("%x", digest.Sum(nil))
			if got != want {
				t.Fatalf("PrepareQuery изменился: %s, ожидалось %s", got, want)
			}
		})
	}
}

func TestBundledCatalogFacetCoverage(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	d := release.Definition()
	store := fake.NewStore()
	store.SeedTaxonomy(d)
	queries := appcatalog.NewQueries(store, fake.NewIndex(), nil, nil, nil, fake.NewClock(time.Time{}))
	for _, id := range []string{"", "shoes", "music", "electronics.smartphones"} {
		t.Run(id, func(t *testing.T) {
			prepared, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{CategoryID: id})
			if err != nil {
				t.Fatal(err)
			}
			if len(prepared.FacetFields) > appcatalog.MaxAttributeFacets {
				t.Fatalf("превышен бюджет фасетов: %d", len(prepared.FacetFields))
			}
			codes := []string{}
			for _, field := range prepared.FacetFields {
				codes = append(codes, field.Code)
			}
			if !sort.StringsAreSorted(codes) {
				t.Fatalf("фасеты не отсортированы по коду: %v", codes)
			}
			if id == "" || id == "shoes" || id == "music" {
				want := []string{"brand", "country"}
				if id == "shoes" {
					want = []string{"color", "size_footwear_label", "season", "target_gender"}
				}
				if id == "music" {
					want = []string{"color"}
				}
				for _, code := range want {
					if !slices.Contains(codes, code) {
						t.Errorf("отсутствует фасет %s: %v", code, codes)
					}
				}
				return
			}
			category, _, ok := d.Category(id)
			if !ok || len(category.Children) != 0 {
				t.Fatal("ожидался лист встроенного каталога")
			}
			fields := d.Fields()
			want := []taxonomy.Attribute{}
			for _, code := range d.Schemas[category.Schema].Attributes {
				field := fields[code]
				if field.Facet && !field.Deprecated {
					want = append(want, field)
				}
			}
			sort.Slice(want, func(i, j int) bool {
				return want[i].Code < want[j].Code
			})
			if !reflect.DeepEqual(prepared.FacetFields, want) {
				t.Fatalf("фасеты листа отличаются от полей схемы: %v вместо %v", prepared.FacetFields, want)
			}
		})
	}
}
