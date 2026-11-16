package taxonomy

import (
	"maps"
	"reflect"
	"slices"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// YAML не содержит вычисленных Values. При изменении только поисковых выражений
// используем опубликованный снимок, чтобы случайно не обновить остальные справочники.
func searchTermsAttributeUpdate(previous, input []taxonomy.Attribute) ([]taxonomy.Attribute, bool) {
	out := slices.Clone(input)
	for i := range out {
		out[i].Values = nil
		out[i].FacetOptions = nil
		out[i].FacetValuesByValue = nil
	}
	if len(previous) != len(input) {
		return out, false
	}
	for index, attribute := range out {
		if input[index].Values != nil {
			return out, false
		}
		current := previous[index]
		next := attribute
		current.Values = nil
		current.FacetOptions = nil
		current.FacetValuesByValue = nil
		current.SearchTermsByValue = nil
		next.SearchTermsByValue = nil
		if !reflect.DeepEqual(current, next) {
			return out, false
		}
	}
	for index := range out {
		out[index].Values = slices.Clone(previous[index].Values)
		out[index].FacetOptions = slices.Clone(previous[index].FacetOptions)
		out[index].FacetValuesByValue = maps.Clone(previous[index].FacetValuesByValue)
		for value, facets := range out[index].FacetValuesByValue {
			out[index].FacetValuesByValue[value] = slices.Clone(facets)
		}
	}
	return out, true
}
