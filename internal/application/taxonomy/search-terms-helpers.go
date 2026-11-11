package taxonomy

import (
	"reflect"
	"slices"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// YAML не содержит вычисленных Values. При изменении только поисковых выражений
// используем опубликованный снимок, чтобы случайно не обновить остальные справочники.
func searchTermsAttributeUpdate(previous, input []taxonomy.Attribute) ([]taxonomy.Attribute, bool) {
	if len(previous) != len(input) {
		return input, false
	}
	out := slices.Clone(input)
	for index, attribute := range input {
		if attribute.Values != nil {
			return input, false
		}
		current := previous[index]
		next := attribute
		current.Values = nil
		current.SearchTermsByValue = nil
		next.SearchTermsByValue = nil
		if !reflect.DeepEqual(current, next) {
			return input, false
		}
		out[index].Values = slices.Clone(previous[index].Values)
	}
	return out, true
}
