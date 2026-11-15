package searchapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/taxonomydata"
)

// Сценарий только читает неизменяемую поставку. Не копируем мегабайтный снимок
// сериализацией fake.Store для каждого из тысяч узлов: здесь проверяется запрос,
// а транзакции и изоляция хранилища проверены отдельными тестами.
type releaseSnapshot struct {
	ports.Tx
	taxonomy.Repository
	definition taxonomy.Definition
}

// TestReleaseSearchLimits использует поставляемые YAML, а не уменьшенную копию схемы.
// TAXONOMY_CONTRACT_DIR сохраняет корпус для проверки настоящим валидатором sc-search.
func TestReleaseSearchLimits(t *testing.T) {
	release, coverage, err := taxonomydata.Load()
	if err != nil {
		t.Fatal(err)
	}
	definition := release.Definition()
	snapshot := releaseSnapshot{definition: definition}
	queries := appcatalog.NewQueries(snapshot, fake.NewIndex(), nil, nil, nil, fake.NewClock(time.Time{}))
	corpus := []offerQuery{}
	report := []string{"category,kind,legacyFacets,facets"}
	globalFields := definition.Fields()
	facetCount := 0
	for _, field := range definition.Attributes {
		if field.Facet {
			facetCount++
		}
	}
	t.Logf(
		"release=%d nodes=%d leaves=%d attributes=%d facets=%d",
		release.Version,
		coverage.Categories,
		coverage.Leaves,
		coverage.Attributes,
		facetCount,
	)
	var walk func(taxonomy.Category, int)
	walk = func(node taxonomy.Category, depth int) {
		prepared, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{
			CategoryID: node.ID,
			Limit:      2,
		})
		if err != nil {
			t.Fatal(err)
		}
		legacyFields := map[string]taxonomy.Attribute{}
		var collect func(taxonomy.Category)
		collect = func(category taxonomy.Category) {
			for _, code := range definition.Schemas[category.Schema].Attributes {
				legacyFields[code] = globalFields[code]
			}
			for _, child := range category.Children {
				collect(child)
			}
		}
		collect(node)
		if depth == 0 {
			legacyFields = globalFields
		}
		legacyFacets := 0
		for _, field := range legacyFields {
			if field.Facet {
				legacyFacets++
			}
		}
		kind := "intermediate"
		switch {
		case depth == 0:
			kind = "root"
		case depth == 1:
			kind = "direction"
		case len(node.Children) == 0:
			kind = "leaf"
		}
		report = append(report, fmt.Sprintf("%s,%s,%d,%d", node.ID, kind, legacyFacets, len(prepared.FacetFields)))
		if len(prepared.FacetFields) > appcatalog.MaxAttributeFacets {
			t.Errorf("%s: exceeded facet budget: %d", node.ID, len(prepared.FacetFields))
		}
		// Независимая граница служебного контракта v5; не лимит глобального справочника.
		if len(prepared.FacetFields) > 256 || len(prepared.Attributes) > 256 {
			t.Errorf(
				"%s: filters=%d facets=%d exceeds search v5",
				node.ID,
				len(prepared.Attributes),
				len(prepared.FacetFields),
			)
		}
		corpus = append(corpus, offerQuery{
			CategoryID:  prepared.CategoryID,
			Limit:       prepared.Limit,
			Sort:        string(prepared.Sort),
			Facets:      wireFacetSelection(prepared.Facets),
			FacetFields: wireFacetFields(prepared),
			Attributes:  prepared.Attributes,
		})
		if depth <= 1 {
			t.Logf("%s: facets=%d -> %d", node.ID, legacyFacets, len(prepared.FacetFields))
		}
		for _, child := range node.Children {
			walk(child, depth+1)
		}
	}
	walk(taxonomy.Category{Children: definition.Categories}, 0)
	if len(corpus) != coverage.Categories+1 {
		t.Fatalf("не все узлы проверены: %d", len(corpus))
	}
	if directory := os.Getenv("TAXONOMY_CONTRACT_DIR"); directory != "" {
		reportData := []byte(strings.Join(report, "\n") + "\n")
		if err := os.WriteFile(filepath.Join(directory, "facets.csv"), reportData, 0600); err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string]any{
			"queries.json":  corpus,
			"snapshot.json": definition,
		} {
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func (s releaseSnapshot) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return fn(ctx, s)
}

func (s releaseSnapshot) Taxonomy() taxonomy.Repository {
	return s
}

func (s releaseSnapshot) Current(context.Context) (taxonomy.Definition, error) {
	return s.definition, nil
}
