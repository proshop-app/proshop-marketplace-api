package ports

import "encoding/json"

// AttributeFilter — OR значений одного кода, AND кодов; границы включительные.
type AttributeFilter struct {
	Values []string
	Min    string
	Max    string
}

// FacetBucket — каноническое значение и число предложений с ним.
type FacetBucket struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// Facet — агрегация по всей отфильтрованной выдаче до пагинации.
type Facet struct {
	Code   string        `json:"code"`
	Name   string        `json:"name"`
	Type   string        `json:"type"`
	Unit   string        `json:"unit,omitempty"`
	Count  int           `json:"count"`
	Values []FacetBucket `json:"values"`
	Min    *json.Number  `json:"min,omitempty"`
	Max    *json.Number  `json:"max,omitempty"`
}
