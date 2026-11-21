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
	"":                                   "e797e08375191de556dfccc6454a0a84020a0593869629a5645a7d95a7dbed94",
	"electronics":                        "5737d732dca7e7799973e2f9d0d5e59687a33f2d7913cdf1f5032d5a2c1f4561",
	"electronics.family-mobile":          "0d478986930f8130b39a38bb5967e2386a975ce3fdb9fd59d08b0b5001b7a484",
	"electronics.smartphones":            "d9e87eddb6d7561ac7cbb8d1ceb314eea2cc96a3eac63f9a82e0b463903f86f1",
	"clothing":                           "aeba73fb01eecd4ed5084ffceb63bce8460ad43a02ef4c97b0c8cd8eab2bf206",
	"clothing.women":                     "bc42e3140f0040fd0df8461ecdf486ae4fbe0081ffafd860770061851b9c4ffe",
	"clothing.women-family-underwear":    "06d65de56c9231d5e1904b4f81051924c732859e1ee12f98012056a9640b6d15",
	"clothing.bras":                      "e033a835d5844a7fe7be4a04e3528ace3d1a1fce0b17f06f9cafee36e9f4addb",
	"auto":                               "8420c467efe3f65befad418c7568abd11d89283923fc3115c0d68ba3e2f87410",
	"auto.family-parts":                  "57cc041056876d78a8ff4ef7954989f0a85d426e037db07e53b0dd05672e6901",
	"auto.family-parts-braking":          "70c7ce383e8374ef5a2651c3e537006b2b06692af1d90ebcf96ef774cf86b1d4",
	"auto.brake-pads":                    "54e3154f4cd2b63ecc975b44dee3309342aff88d14519546d8ec0a1f084290ec",
	"pets":                               "5e0d5b96e65dbba04e00160552b53e597884d745b2ce9b9258f34ec5d9b74c5d",
	"pets.family-food":                   "97d23848219b25d42b9674122e1b92344a1e909d478cb9507df965c45c5a2e6b",
	"pets.pet-treats":                    "4070eb150035dddc88dc4968a3bceea1a03b70eda270df2912fea2634a31b4de",
	"industry":                           "baa9035316966f9a31a4925c76e819f12cdb56f865447672eed21684912a65aa",
	"industry.family-equipment":          "654b61105db59b075796c23e62473ace53abd31cd8014db321f060959035eac0",
	"industry.family-equipment-machines": "d4e5d3511d58eacc7f9f4e22b6e4e8894a2c737a6d233be40061969fb3119c6c",
	"industry.soldering-stations":        "d1ff67ec89abc3550cee394e384dd9f83fb1236f1f623ed6ca655915b69325c9",
	"food":                               "1ab2d73efb386733a62195901e6baaf109cc78bb5134bdc27b7dd4055d9830f0",
	"food.grains":                        "8e022962c8d6a7a2207805de61114b81a214663fa2ccbdd7104da8d9b12e4a66",
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
				if field.Facet && !field.Deprecated && !slices.Contains(d.Schemas[category.Schema].HiddenFacets, code) {
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
