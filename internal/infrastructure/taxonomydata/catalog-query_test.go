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
	"":                                   "9c102e23bdc9da5b63a0e770540a53464fcb8559a27a6f2c4cc37dadd3fb142e",
	"electronics":                        "ffb4fec5bbff919123e6401466cb3cda6f525eaaf5bbe2fac95b10ef6a3a53da",
	"electronics.family-mobile":          "c35df6c34a1ddc303eeefe8e254c28df2aaef1d3d1f5a9a97ec97dcfdd6d31b4",
	"electronics.smartphones":            "3cbd98e35c515deb0c0ca25396cf9dd11932882b0eec8ec43be544e340c226dc",
	"clothing":                           "71ff16adbec3b4f85fb9d34540a03fba8368a2de3d779f476c194b24b826fb94",
	"clothing.women":                     "b316b97702f9c0dfc4e6cc5460a718c7e43becad062ee2d93edc84503e77078f",
	"clothing.women-family-underwear":    "45774a729029bc21ed86e4f470124ab973d3ae699a1eeacfe3436804fe4d9d70",
	"clothing.bras":                      "61747b7c6aaf1f3bab3acb0f56cc2f6b1d95e2e295e396ba2a0a019ac41e4382",
	"auto":                               "e41a070feea1baded5e810be1d80e56eaad0c4223224e86642bb4c4d68b3a26b",
	"auto.family-parts":                  "0dab705d708432214a6406db49e3aa799c1b258f342fce854c5a7096a1394393",
	"auto.family-parts-braking":          "d69bd768cc8a43e7eeea1fa83fe6bc3aa55c77154adb4464bf9554608d09bd05",
	"auto.brake-pads":                    "964d5d14135c2b636b8d6d37f81c1243e2583097bb6c8ba9d85cacfd3425f3f7",
	"pets":                               "5aa0c86d06fcdc19344deae5ee05683edb434082125de585e343d6f741285034",
	"pets.family-food":                   "c187e33fbfdde3e160ca314fcfe6045612a51032105f6b627934e9a66ff07dc7",
	"pets.pet-treats":                    "4b448acf8176ed6c0753d91a7f162fb98c543174cb9950e8fc4f3e4ee1d24206",
	"industry":                           "6d2e6105dc8de066be335f74123b6dded1a0b892e523e885fa10fa4ab18eff1f",
	"industry.family-equipment":          "49379362986d7c775ec907ca789ce6fd4d3b9f78f96a0dcc87aa4dd950ae8173",
	"industry.family-equipment-machines": "f6311a8d6a481f83f11dfcf6f6378696bf2511dccadbf6bfb1206174388f1d55",
	"industry.soldering-stations":        "b0d472fe9d0e4a5dd1ca923ef1fe0e3511487a3cf6d262f3a836397d26f126eb",
	"food":                               "f645369735b76576ce2ef8353571ceae5b04d91a7183bc2ea6c2111ba087dd38",
	"food.grains":                        "15b5bad5b75d9f2ead4d3e04aa20948d2b1afef922a233037092d106d6620900",
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
