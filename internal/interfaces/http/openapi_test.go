package http

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const taxonomyData = "../../infrastructure/taxonomydata/data/"

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
