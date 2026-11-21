package ports

import (
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// AttributeFilter — OR значений одного кода, AND кодов; границы включительные.
type AttributeFilter struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
	Min    string   `json:"min"`
	Max    string   `json:"max"`
}

// FacetBucket — каноническое значение и число предложений с ним.
// Meta — образец значения из снимка таксономии (valuesMeta): витрина рисует палитру
// по самому фасету, без схемы категории и справочника атрибутов.
type FacetBucket struct {
	Value string              `json:"value"`
	Count int                 `json:"count"`
	Meta  *taxonomy.ValueMeta `json:"meta,omitempty"`
}

// PriceBucket — границы в копейках из гистограммы движка.
type PriceBucket struct {
	From  int64 `json:"from"`
	To    int64 `json:"to"`
	Count int   `json:"count"`
}

// Facet — агрегация по всей отфильтрованной выдаче до пагинации.
type Facet struct {
	Primary bool          `json:"-"`
	Buckets []PriceBucket `json:"buckets,omitempty"`
	Code    string        `json:"code"`
	Name    string        `json:"name"`
	Type    string        `json:"type"`
	Unit    string        `json:"unit,omitempty"`
	Count   int           `json:"count"`
	Values  []FacetBucket `json:"values"`
	Min     *json.Number  `json:"min,omitempty"`
	Max     *json.Number  `json:"max,omitempty"`
}
