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
	"":                                   "f8ceeefc8fb9fb15a9a9ca6b0e14e1577878de8a6218fafc0d9f37861a6bdd78",
	"electronics":                        "e84f39ed30ff4765ac3bab64079a8aab13a02ceafd0b70e61be85c97f6329944",
	"electronics.family-mobile":          "cb7c03f2ae393c80e8d8f4bcb2af47819130250178582fd47f0cd0874bc13fa1",
	"electronics.smartphones":            "6cbcff764985c4d519090cdebcc4f65576951cb87d7be5511b12bdd6b720e56c",
	"clothing":                           "c58b10dfa4042b91abf847aadec4b8690a1d367e3a5828d0b1dcbbda16f0ae23",
	"clothing.women":                     "669d25c24ba7fcddb0c020a3a8cb2e6b5c14dc1f5b75bcc01e1cea899b8862fe",
	"clothing.women-family-underwear":    "9a4d7d1bd51fb002ebf426d4cfc0dfe58a77e4817fe8f11319985831042c1e2e",
	"clothing.bras":                      "761cf3c544ce042f02f0cee4f916edb4e068afa163c0cceac7b58e49ca2e9b1e",
	"auto":                               "d3b262617641d3b524949b0393df17765007b8179a628a8b84036207a88ade79",
	"auto.family-parts":                  "3476aac394f8b9db3b447961b71aafdeac7839e20de40936d1fd5c831760eef2",
	"auto.family-parts-braking":          "3facbe579fe4689cbd412630e90345d3820cdc115de4ca67b682a0893c795d6d",
	"auto.brake-pads":                    "8cc7758ff5c200433f60daaa3ac41a2d4312ad2f2b441265ed8fab98a1620e66",
	"pets":                               "5c26c3ab2c54dbe232ea6960252ab526fc3c3b9b9a0d15668bc346c7e88b5f55",
	"pets.family-food":                   "7755d29238540b2495b69bc29891ded561cdad927ea97574c052969b4c9a50ca",
	"pets.pet-treats":                    "c74c43cc343f95ba7de738e0ecfdcaaa1c1515aabcafe126103c6143304240d6",
	"industry":                           "410f8db8fbc3cf667c5dc4b48c64f94054f9a653a597746330ab782914fe4be0",
	"industry.family-equipment":          "6fce5f7e47fc2a9747c1e3e0e9ab8f867b6868678b74935fbbeec6b92ee111d4",
	"industry.family-equipment-machines": "7df6027c8f4a210a68ff0c7f34686ed8574e7e1ef8b368ee5c5f622e48e7bc0b",
	"industry.soldering-stations":        "07985b6f31d78a6dd5240be99b89c7c423c2ab4ca41f8dbcc90f8a1088f976d9",
	"food":                               "3134d1bc44cd026db2ff520bd71a864d3b60560d2af2881a8d1d6bccce758302",
	"food.grains":                        "a8140428d65c0c0d50361762c4da1f0f8a63f290d2e18446f167ec15c9279e54",
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
