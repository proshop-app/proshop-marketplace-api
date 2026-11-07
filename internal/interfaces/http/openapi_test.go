package http

import (
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const taxonomyData = "../../infrastructure/taxonomydata/data/"

// Якоря и ссылки дампера (&id001, *id001): повтор имени якоря yaml.v3
// терпит, а PyYAML и генераторы клиентов — нет.
var dumperAnchor = regexp.MustCompile(`(^|\s)[&*]id\d+`)

type spec struct {
	Paths      map[string]map[string]any `yaml:"paths"`
	Components map[string]map[string]any `yaml:"components"`
}

type bundledCategory struct {
	ID       string            `json:"id"`
	Children []bundledCategory `json:"children"`
}

type bundledAttribute struct {
	Code string `json:"code"`
}

func loadSpec(t *testing.T) (spec, []byte) {
	t.Helper()

	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var doc spec
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("api/openapi.yaml не разбирается: %v", err)
	}

	return doc, raw
}

// TestOpenAPICoversRoutes — каждый маршрут описан в api/openapi.yaml, и
// в описании нет маршрутов, которых нет в коде.
func TestOpenAPICoversRoutes(t *testing.T) {
	doc, _ := loadSpec(t)

	documented := map[string]bool{}
	for path, operations := range doc.Paths {
		for method := range operations {
			documented[strings.ToUpper(method)+" "+path] = true
		}
	}

	served := map[string]bool{}
	for _, r := range routes(Deps{}) {
		served[r.method+" "+r.path] = true
	}

	var missing, extra []string
	for key := range served {
		if !documented[key] {
			missing = append(missing, key)
		}
	}
	for key := range documented {
		if !served[key] {
			extra = append(extra, key)
		}
	}

	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("api/openapi.yaml расходится с маршрутами:\nне описаны: %v\nнет в коде: %v", missing, extra)
	}
}

// TestOpenAPIRefsResolve — все $ref указывают на существующие компоненты.
func TestOpenAPIRefsResolve(t *testing.T) {
	doc, raw := loadSpec(t)

	for _, line := range strings.Split(string(raw), "\n") {
		_, ref, found := strings.Cut(line, `$ref: "#/components/`)
		if !found {
			continue
		}

		ref, _, _ = strings.Cut(ref, `"`)
		kind, name, _ := strings.Cut(ref, "/")

		if _, ok := doc.Components[kind][name]; !ok {
			t.Errorf("ссылка на несуществующий компонент: %s/%s", kind, name)
		}
	}
}

// TestOpenAPIHasNoAnchors — в описании нет якорей и ссылок YAML: каждое
// значение записано явно, файл разбирает любой парсер.
func TestOpenAPIHasNoAnchors(t *testing.T) {
	_, raw := loadSpec(t)

	for number, line := range strings.Split(string(raw), "\n") {
		if dumperAnchor.MatchString(line) {
			t.Errorf("api/openapi.yaml:%d: якорь или ссылка YAML: %s", number+1, strings.TrimSpace(line))
		}
	}
}

// TestOpenAPITaxonomyPatternsAcceptBundle — шаблоны id категории и кода
// атрибута в спецификации принимают настоящую поставку: клиент, собранный
// по OpenAPI, не должен отклонять дерево, которое отдаёт сервер.
func TestOpenAPITaxonomyPatternsAcceptBundle(t *testing.T) {
	doc, _ := loadSpec(t)

	idPattern := schemaPattern(t, doc, "TaxonomyCategory", "id")
	codePattern := schemaPattern(t, doc, "TaxonomyAttribute", "code")

	var categories []bundledCategory
	readBundle(t, "categories.yaml", &categories)

	var attributes []bundledAttribute
	readBundle(t, "attributes.yaml", &attributes)

	pending := categories
	for len(pending) > 0 {
		category := pending[0]
		pending = append(pending[1:], category.Children...)

		if !idPattern.MatchString(category.ID) {
			t.Errorf("id категории поставки не проходит шаблон OpenAPI: %q", category.ID)
		}
	}

	for _, attribute := range attributes {
		if !codePattern.MatchString(attribute.Code) {
			t.Errorf("код атрибута поставки не проходит шаблон OpenAPI: %q", attribute.Code)
		}
	}
}

func schemaPattern(t *testing.T, doc spec, schema, property string) *regexp.Regexp {
	t.Helper()

	properties, _ := doc.Components["schemas"][schema].(map[string]any)["properties"].(map[string]any)
	field, _ := properties[property].(map[string]any)

	pattern, ok := field["pattern"].(string)
	if !ok {
		t.Fatalf("у %s.%s нет pattern", schema, property)
	}

	return regexp.MustCompile(pattern)
}

func readBundle(t *testing.T, name string, target any) {
	t.Helper()

	raw, err := os.ReadFile(taxonomyData + name)
	if err != nil {
		t.Fatal(err)
	}

	if err := yaml.Unmarshal(raw, target); err != nil {
		t.Fatalf("%s не разбирается: %v", name, err)
	}
}

func TestOpenAPIShopShowcaseAndOrderSeller(t *testing.T) {
	doc, _ := loadSpec(t)
	schemas := doc.Components["schemas"]
	for _, name := range []string{"ShopHeader", "CartLine", "CheckoutGroup"} {
		field := "seller"
		ref := "#/components/schemas/OrderSeller"
		if name == "ShopHeader" {
			field = "showcase"
		}
		schema := schemas[name].(map[string]any)
		required := schema["required"].([]any)
		found := false
		for _, key := range required {
			found = found || key == field
		}
		if !found {
			t.Fatalf("%s: поле %s обязательно", name, field)
		}
		property := schema["properties"].(map[string]any)[field].(map[string]any)
		if field == "showcase" {
			if property["type"] != "array" || property["maxItems"] != 3 {
				t.Fatal(property)
			}
			property = property["items"].(map[string]any)
			ref = "#/components/schemas/ShopImage"
		}
		if property["$ref"] != ref {
			t.Fatal(property)
		}
	}
	seller := schemas["OrderSeller"].(map[string]any)
	types := seller["type"].([]any)
	if len(types) != 2 || types[0] != "object" || types[1] != "null" {
		t.Fatal(types)
	}
	properties := seller["properties"].(map[string]any)
	if len(properties) != 2 || properties["slug"] == nil || properties["name"] == nil {
		t.Fatal(properties)
	}
}

func TestOpenAPIBuyerSchemas(t *testing.T) {
	doc, _ := loadSpec(t)
	schemas := doc.Components["schemas"]
	for _, name := range []string{"BuyerProfile", "BuyerFavorites", "BuyerPriceWatches", "BuyerSubscriptions"} {
		if _, ok := schemas[name]; !ok {
			t.Fatalf("нет схемы %s", name)
		}
	}
	favorite := schemas["BuyerFavorite"].(map[string]any)["allOf"].([]any)
	if favorite[0].(map[string]any)["$ref"] != "#/components/schemas/Offer" {
		t.Fatal("нет полей Offer")
	}
	properties := favorite[1].(map[string]any)["properties"].(map[string]any)
	if properties["listed"].(map[string]any)["type"] != "boolean" {
		t.Fatal("нет listed")
	}
	offer := schemas["Offer"].(map[string]any)["properties"].(map[string]any)
	if offer["available"].(map[string]any)["type"] != "integer" {
		t.Fatal("остаток должен быть целым")
	}
	header := schemas["ShopHeader"].(map[string]any)["properties"].(map[string]any)
	if header["subscribers"] == nil || header["subscribed"] == nil {
		t.Fatal("нет подписчиков в шапке")
	}
}

func TestOpenAPIBuyerContractFields(t *testing.T) {
	doc, _ := loadSpec(t)
	schemas := doc.Components["schemas"]
	header := schemas["ShopHeader"].(map[string]any)
	required := header["required"].([]any)
	for _, name := range []string{"rating", "showcase", "subscribers"} {
		if !slices.Contains(required, any(name)) {
			t.Fatalf("ShopHeader.required: нет %s", name)
		}
	}
	if slices.Contains(required, any("subscribed")) {
		t.Fatal("subscribed обязателен для гостя")
	}
	profile := schemas["BuyerProfile"].(map[string]any)["properties"].(map[string]any)
	alerts := profile["alerts"].(map[string]any)
	if !slices.Contains(alerts["required"].([]any), any("priceDrops")) {
		t.Fatal(alerts)
	}
	drops := alerts["properties"].(map[string]any)["priceDrops"].(map[string]any)
	if !reflect.DeepEqual(drops["type"], []any{"integer", "null"}) || drops["minimum"] != 0 {
		t.Fatal(drops)
	}
	for _, name := range []string{"BuyerNotifications", "BuyerNotificationsPatch"} {
		schema := schemas[name].(map[string]any)
		if schema["additionalProperties"] != false {
			t.Fatal(schema)
		}
		for _, property := range schema["properties"].(map[string]any) {
			if _, ok := property.(map[string]any)["default"]; ok {
				t.Fatal("PATCH не должен подставлять default")
			}
		}
		if name == "BuyerNotifications" {
			if !reflect.DeepEqual(schema["required"], []any{"priceDrops", "shopNews"}) {
				t.Fatal(schema)
			}
		} else if schema["required"] != nil {
			t.Fatal(schema)
		}
	}
	patch := schemas["BuyerProfilePatch"].(map[string]any)["properties"].(map[string]any)
	if patch["notifications"].(map[string]any)["$ref"] != "#/components/schemas/BuyerNotificationsPatch" ||
		profile["notifications"].(map[string]any)["$ref"] != "#/components/schemas/BuyerNotifications" {
		t.Fatal("схемы уведомлений перепутаны")
	}
	for path, method := range map[string]string{
		"/api/v1/buyer/favorites":               "get",
		"/api/v1/buyer/price-watches":           "get",
		"/api/v1/buyer/favorites/{offerId}":     "put",
		"/api/v1/buyer/price-watches/{offerId}": "put",
	} {
		operation := doc.Paths[path][method].(map[string]any)
		if operation["responses"].(map[string]any)["503"] == nil {
			t.Fatal(path, "нет 503")
		}
	}
}
