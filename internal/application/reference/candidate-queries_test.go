package reference

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestCandidateQueriesSplitRootsAndKeepExclusions(t *testing.T) {
	scope := taxonomy.CandidateScope{
		Attribute:         "a1",
		Dictionaries:      []string{"d1"},
		ExcludeCategories: []string{"c1.x"},
	}
	for i := 0; i <= ports.MaxCandidateCategories; i++ {
		scope.Categories = append(scope.Categories, fmt.Sprintf("c%d", i))
	}
	queries, skipped := candidateQueries(taxonomy.Definition{}, scope)
	if len(skipped) != 0 || len(queries) != 2 || len(queries[0].Categories) != 32 || len(queries[1].Categories) != 1 {
		t.Fatal(queries, skipped)
	}
	for _, query := range queries {
		if !reflect.DeepEqual(query.ExcludeCategories, scope.ExcludeCategories) || query.Attribute != "a1" {
			t.Fatal("разбиение изменило фильтр", query)
		}
	}
}

func TestCandidateQueriesSplitGlobalAndSkipOnlyOversizedRoot(t *testing.T) {
	scope := taxonomy.CandidateScope{Attribute: "a1", Dictionaries: []string{"d1"}}
	d := taxonomy.Definition{Categories: []taxonomy.Category{{ID: "c1"}, {ID: "c2"}, {ID: "c3"}}}
	for i := 0; i <= ports.MaxCandidateCategories; i++ {
		id := fmt.Sprintf("c1.x%d", i)
		scope.ExcludeCategories = append(scope.ExcludeCategories, id)
		d.Categories[0].Children = append(d.Categories[0].Children, taxonomy.Category{ID: id})
	}
	scope.ExcludeCategories = append(scope.ExcludeCategories, "c3")
	queries, skipped := candidateQueries(d, scope)
	if len(queries) != 1 || !reflect.DeepEqual(queries[0].Categories, []string{"c2"}) ||
		len(queries[0].ExcludeCategories) != 0 || len(skipped) != 1 ||
		!reflect.DeepEqual(skipped[0].Categories, []string{"c1"}) {
		t.Fatal(queries, skipped)
	}
	// Те же исключения в двух корнях помещаются после разделения, терять их нельзя.
	d.Categories[0].Children = d.Categories[0].Children[:16]
	d.Categories[1].Children = nil
	scope.ExcludeCategories = scope.ExcludeCategories[:16]
	for i := 0; i < 17; i++ {
		id := fmt.Sprintf("c2.x%d", i)
		d.Categories[1].Children = append(d.Categories[1].Children, taxonomy.Category{ID: id})
		scope.ExcludeCategories = append(scope.ExcludeCategories, id)
	}
	scope.Categories = []string{"c1", "c2"}
	queries, skipped = candidateQueries(d, scope)
	if len(skipped) != 0 || len(queries) != 2 || len(queries[0].ExcludeCategories) != 16 ||
		len(queries[1].ExcludeCategories) != 17 {
		t.Fatal(queries, skipped)
	}
}
