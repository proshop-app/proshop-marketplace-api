package catalog

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestPinnedFacetVisibility(t *testing.T) {
	pinned, unpinned := true, false
	for _, tc := range []struct {
		code    string
		setting *bool
		pinned  bool
	}{
		{code: "color", pinned: true},
		{code: "color", setting: &pinned, pinned: true},
		{code: "color", setting: &unpinned},
		{code: "a1"},
		{code: "a1", setting: &pinned, pinned: true},
		{code: "a1", setting: &unpinned},
	} {
		for _, selected := range []bool{false, true} {
			query := ports.OfferQuery{
				FacetFields: []taxonomy.Attribute{{Code: tc.code, Type: "enum", Facet: true, FacetPinned: tc.setting}},
			}
			if selected {
				query.Attributes = map[string]ports.AttributeFilter{tc.code: {Values: []string{"v1"}}}
			}
			for _, count := range []int{0, 1, 19, 20, 100} {
				for _, size := range []int{0, 1, 2} {
					facet := ports.Facet{
						Code:   "attr." + tc.code,
						Count:  count,
						Values: make([]ports.FacetBucket, size),
					}
					want := count >= FacetCoveragePercent
					if tc.pinned {
						want = selected || (size >= 2 && count > 0)
					}
					if got := visibleAttributeFacet(facet, 100, query); got != want {
						t.Fatalf(
							"код=%s закреплён=%v выбран=%v охват=%d значений=%d: %v",
							tc.code,
							tc.pinned,
							selected,
							count,
							size,
							got,
						)
					}
				}
			}
		}
	}
}
