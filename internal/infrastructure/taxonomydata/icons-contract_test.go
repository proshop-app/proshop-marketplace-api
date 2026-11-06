package taxonomydata

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestCategoryIconsMatchOpenAPI(t *testing.T) {
	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	category := schemas["TaxonomyCategory"].(map[string]any)
	properties := category["properties"].(map[string]any)
	icon := properties["icon"].(map[string]any)
	values := icon["enum"].([]any)
	seen := map[string]bool{}
	for _, value := range values {
		name := value.(string)
		if seen[name] || !taxonomy.ValidIcon(name) {
			t.Fatalf("неизвестная или повторная иконка %s", name)
		}
		seen[name] = true
	}
	if len(seen) != len(taxonomy.CategoryIcons) {
		t.Fatal("наборы иконок OpenAPI и домена различаются")
	}
}
