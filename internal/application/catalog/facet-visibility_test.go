package catalog

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestPinnedFacetVisibility(t *testing.T) {
	for _, code := range []string{"color", "a1"} {
		for _, pinned := range []bool{false, true} {
			for _, selected := range []bool{false, true} {
				query := ports.OfferQuery{
					FacetFields: []taxonomy.Attribute{{Code: code, Type: "enum", Facet: true, FacetPinned: pinned}},
				}
				if selected {
					query.Attributes = map[string]ports.AttributeFilter{code: {Values: []string{"v1"}}}
				}
				for _, count := range []int{0, 1, 19, 20, 100} {
					for _, size := range []int{0, 1, 2} {
						facet := ports.Facet{
							Code:   "attr." + code,
							Count:  count,
							Values: make([]ports.FacetBucket, size),
						}
						want := count >= FacetCoveragePercent
						if pinned {
							want = selected || (size >= 2 && count > 0)
						}
						if got := visibleAttributeFacet(facet, 100, query); got != want {
							t.Fatalf(
								"код=%s закреплён=%v выбран=%v охват=%d значений=%d: %v",
								code,
								pinned,
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
}
