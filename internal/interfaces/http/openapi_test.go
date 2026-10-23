package http

import (
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type spec struct {
	Paths      map[string]map[string]any `yaml:"paths"`
	Components map[string]map[string]any `yaml:"components"`
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
