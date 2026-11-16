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
	"":                                   "1934070f51c39e0f2b51f1653029fc3f32c263c0941831c5eede603368ecae15",
	"electronics":                        "c4c7270d73531fdb9efe2f66990cf787b5f3819bb002fc7635dd8b5dcaa89612",
	"electronics.family-mobile":          "c1f61df1303634b02f3f0f39315bacc03357fc2fa1520530fd85fbdc85571984",
	"electronics.smartphones":            "c0854a806b0010c89312dc8f70aef213eb1bb30fbeda5340c87ae5cc6a26d815",
	"clothing":                           "2617026089f3466cb2a6e43958e6c07419276f0b1204dee23fe9793653f07ff0",
	"clothing.women":                     "c04acc320568387cfb193d6e02d407344beb0b3bf767f1fc3ec25b6fa87a2610",
	"clothing.women-family-underwear":    "5d38c42353a2674ae63210b5a329bdd32856cbe52711794e59e4c9944b203077",
	"clothing.bras":                      "0f5d965718c36acb758acfb15b5ce1430297f3d34594beb38c11e5ed03c65051",
	"auto":                               "f36db7e2365737b0d1dd7a9f2672de392558f5467169472b48ec3e290c63d2e3",
	"auto.family-parts":                  "8783a7081d361d5164ff16785a90d8e28fc77ca62c1a51dea3682c0fdf5c4226",
	"auto.family-parts-braking":          "4402c074c53694f808a825fd877d7efbdda0bcf45188b4c1c193857d288a9187",
	"auto.brake-pads":                    "a098847d78b52b54b0bad132cee03d05b83045487e7e778fc77517af19986c44",
	"pets":                               "217e951ff0a73fe04c1143ffd08f3989c627464d108538004aa74d977fe36a51",
	"pets.family-food":                   "ba6a5665d73d7e49b94ccef358965935a542a6e23c2daa786bad0925cb1d8560",
	"pets.pet-treats":                    "36dbade10c814274433282bbe8047490ebc06bf25dd84c57794c86d8d2aeca3e",
	"industry":                           "c0202b91663763a6d164e5d0d467a2a3e8fd11f7a14ea1bb0d95eac23db4249a",
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
