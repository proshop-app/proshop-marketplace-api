package taxonomy

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestSearchTermsUpdateCopiesFacetMapping(t *testing.T) {
	previous := []taxonomy.Attribute{{
		Code:               "a1",
		Values:             []string{"v1"},
		FacetOptions:       []string{"f1"},
		FacetValuesByValue: map[string][]string{"v1": {"f1"}},
	}}
	next, frozen := searchTermsAttributeUpdate(previous, []taxonomy.Attribute{{Code: "a1"}})
	if !frozen {
		t.Fatal("снимок не сохранён")
	}
	next[0].FacetValuesByValue["v1"][0] = "f2"
	next[0].FacetValuesByValue["v2"] = []string{"f3"}
	if previous[0].FacetValuesByValue["v1"][0] != "f1" || len(previous[0].FacetValuesByValue) != 1 {
		t.Fatal("новый снимок изменил исходное отображение фасета")
	}
}
