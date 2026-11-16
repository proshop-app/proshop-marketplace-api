package taxonomy

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"
)

// ValidateFacetValues проверяет общий контракт поиска, без знания о цветах.
func ValidateFacetValues(values []string) error {
	if len(values) < 1 || len(values) > 8 {
		return ErrInvalid.WithMessage("значения фасета — от 1 до 8 строк")
	}
	seen := map[string]bool{}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || seen[value] || utf8.RuneCountInString(value) > 1024 {
			return ErrInvalid.WithMessage("пустое, повторное или слишком длинное значение фасета")
		}
		seen[value] = true
	}
	return nil
}

func (a Attribute) facetValues(raw json.RawMessage) []string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return slices.Clone(a.FacetValuesByValue[value])
}
