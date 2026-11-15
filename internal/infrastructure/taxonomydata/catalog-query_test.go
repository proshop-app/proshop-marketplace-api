package taxonomydata

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// Байты PrepareQuery и текст ошибок зафиксированы до изменения scopeFields на встроенной поставке.
// Проверяем все поля FacetFields, порядок, типы фильтров, nil/пустые значения и остальные поля запроса.
var bundledQueryDigests = map[string]string{
	"":                                   "8ca182740c841b228f519599276da5dc7bcc5f28044a221c7f038bce4444819f",
	"electronics":                        "820f16e7efab67849ce5f25c5dd375d550e48dbeadcb9b4c1a2abbf8d4e4e1eb",
	"electronics.family-mobile":          "cd8142cb5dfb102a918613b9158d027d49528af6ceb837a519e7cefe5a69edcd",
	"electronics.smartphones":            "ca30b7646f5cc4ae6fbd8a2ea9e530e8ef6e76814ef2b8cd47bb3226ed1e8771",
	"clothing":                           "0439e97c6a8bb3a80184850d93b889c1a32d49c55916aa77dd135bc2dacde8f6",
	"clothing.women":                     "a0562698004cfad164c4362350f44eccb6005aced681946642b99f6ba90faad5",
	"clothing.women-family-underwear":    "8e58e2fcfe1d60a14d452c34ee654ed2c79164345a9d51d8cbd17732e27470cf",
	"clothing.bras":                      "28fd2a95b5f7ce3eeda0f768fdabddeb90059cc6cd82402b3873b56e223ba95a",
	"auto":                               "cf9b73e695c0a4affc2f9ddd87df0855943766f92389d5c0617de62bc9152a4c",
	"auto.family-parts":                  "d83040a7e31dc1177c8b640375875f681024007408e20921a9438c5bdbfe5397",
	"auto.family-parts-braking":          "4402c074c53694f808a825fd877d7efbdda0bcf45188b4c1c193857d288a9187",
	"auto.brake-pads":                    "a098847d78b52b54b0bad132cee03d05b83045487e7e778fc77517af19986c44",
	"pets":                               "5637a7c2c9b64bd498a02dbdfcd149a8d033f00fcf32354acb267580d0618294",
	"pets.family-food":                   "ba6a5665d73d7e49b94ccef358965935a542a6e23c2daa786bad0925cb1d8560",
	"pets.pet-treats":                    "36dbade10c814274433282bbe8047490ebc06bf25dd84c57794c86d8d2aeca3e",
	"industry":                           "6269235ec807b8f2822f846c9d9a95dc109db9b297be06e175da5eba026dd41d",
	"industry.family-equipment":          "6ea4414533f682c4b5b1c0bbcf4e8343678f6986a85f0dc366985e35e199212f",
	"industry.family-equipment-machines": "e7094e61fad8bf3bed9a5c19ca120eeb123f31debb3d6a6edf62800c3b261e2c",
	"industry.soldering-stations":        "8ad8e8af7fccd7079ba9c11d7fc78fc634af750a405c3d3eeafb0faec788cba5",
	"food":                               "c62ffa44e5ac7ab85434acbc4af587b61271306787485a9bf785862be3c8db9d",
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
