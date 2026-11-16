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
	"":                                   "1171099a0845703428b3da3922c2f22baead6fb8e2cab5d80ccd347e660f1cd7",
	"electronics":                        "7539365e9db4a8227c367cd6ffb89e84fdbd0d47f3c411cd3272051d0d904d69",
	"electronics.family-mobile":          "681359fd424f484683a140fa7692544b571a5d330a9d8738518371cdb60b5d7b",
	"electronics.smartphones":            "ca30b7646f5cc4ae6fbd8a2ea9e530e8ef6e76814ef2b8cd47bb3226ed1e8771",
	"clothing":                           "e013925a0c89057d999b803d6c274c0263771247a61914f19705459184df0929",
	"clothing.women":                     "659c7f3fbb2f6f55aca011a84e749569b7ff9ac5c9ca8d7a4fdcf9be9a2e55c3",
	"clothing.women-family-underwear":    "9bc8b88ea2741448fd2daa25e592f1f4b099e7bc468177d2495b2ec1d9dea11a",
	"clothing.bras":                      "a3027e8b5f510f4e0f8ceed1c7f849dd1e6df71568c7b6f40da759d270de6377",
	"auto":                               "db21956b22c81d10aeeb3d0228eea10e86dfd0cc3cfda2d40d492401800891d2",
	"auto.family-parts":                  "bc9b400497e384d2631f22a7b279e25b882cab4b08f7e8057d4a2daebb1ae143",
	"auto.family-parts-braking":          "4402c074c53694f808a825fd877d7efbdda0bcf45188b4c1c193857d288a9187",
	"auto.brake-pads":                    "a098847d78b52b54b0bad132cee03d05b83045487e7e778fc77517af19986c44",
	"pets":                               "6841cf361a477ba45ca799ff57b6428b0883dc3604953416ff8e5c7096b13ffa",
	"pets.family-food":                   "ba6a5665d73d7e49b94ccef358965935a542a6e23c2daa786bad0925cb1d8560",
	"pets.pet-treats":                    "36dbade10c814274433282bbe8047490ebc06bf25dd84c57794c86d8d2aeca3e",
	"industry":                           "6e0f761d31b5c9bfff952af0c238ad4455df42a42241f6b9db82b92425567c91",
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
