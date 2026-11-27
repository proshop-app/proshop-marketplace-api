package catalog

import (
	"cmp"
	"context"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// AttributeDisplay дополняет каноническое значение карточки сведениями для покупателя.
type AttributeDisplay struct {
	Name string              `json:"name"`
	Meta *taxonomy.ValueMeta `json:"meta,omitempty"`
}

type taxonomyDisplay struct {
	definition taxonomy.Definition
	fields     map[string]taxonomy.Attribute
	sources    map[string]map[string]string
	panels     map[string]facetPanel
}

type facetDisplay struct {
	taxonomy *taxonomyDisplay
	sources  map[string][]string
	order    []taxonomy.Filter
}

type valueOrder struct {
	position *int
	first    float64
	last     float64
	numeric  bool
}

// RefreshTaxonomyDisplay прогревает снимок для карточек и вариантов вне HTTP-запросов.
func (q *Queries) RefreshTaxonomyDisplay(
	ctx context.Context, load func(context.Context, int64) (taxonomy.Definition, error),
) error {
	known := int64(-1)
	if current := q.display.Load(); current != nil {
		known = current.definition.Version
	}
	definition, err := load(ctx, known)
	if err == nil && definition.Version > known {
		q.displayFor(definition)
	}
	return err
}

func (q *Queries) displayFor(definition taxonomy.Definition) *taxonomyDisplay {
	if current := q.display.Load(); current != nil && current.definition.Version == definition.Version {
		return current
	}
	q.displayMu.Lock()
	defer q.displayMu.Unlock()

	current := q.display.Load()
	if current != nil && current.definition.Version == definition.Version {
		return current
	}
	view := &taxonomyDisplay{
		definition: definition,
		fields:     definition.Fields(),
		sources:    definition.SelectionSources(),
	}
	view.panels = view.buildFacetPanels()
	if current == nil || current.definition.Version < definition.Version {
		q.display.Store(view)
	}
	return view
}

func (v *taxonomyDisplay) attributes(doc ports.OfferDocument) map[string]AttributeDisplay {
	if v == nil {
		return nil
	}
	out := make(map[string]AttributeDisplay, len(doc.Attributes))
	for code, raw := range doc.Attributes {
		field, exists := v.fields[code]
		if !exists {
			continue
		}
		var value string
		var meta *taxonomy.ValueMeta
		if (field.Type == "enum" || field.Type == "string") && json.Unmarshal(raw, &value) == nil {
			metadata := field.ValuesMeta
			if field.DictionarySelection {
				source := v.sources[doc.CategoryID][code]
				metadata = v.definition.SelectionValues[source].ValuesMeta
			}
			meta = metadata[value].DisplayMeta()
		}
		out[code] = AttributeDisplay{Name: field.BuyerName(), Meta: meta}
	}
	return out
}

func (v facetDisplay) meta(field taxonomy.Attribute, value string) taxonomy.ValueMeta {
	if field.DictionarySelection && v.taxonomy != nil {
		return v.taxonomy.definition.SharedValueMeta(v.sources[field.Code], value)
	}
	return field.ValuesMeta[value]
}

func (v *taxonomyDisplay) presentVariantOption(option *VariantOption, offers []ports.OfferDocument) {
	if v == nil {
		return
	}
	field, found := v.fields[option.Code]
	if !found {
		return
	}
	option.Name = field.BuyerName()
	var dictionaries []string
	if field.DictionarySelection {
		for _, offer := range offers {
			source := v.sources[offer.CategoryID][field.Code]
			if source != "" && !slices.Contains(dictionaries, source) {
				dictionaries = append(dictionaries, source)
			}
		}
	}
	metadata := make(map[string]taxonomy.ValueMeta, len(option.Values))
	for _, value := range option.Values {
		meta := field.ValuesMeta[value]
		if field.DictionarySelection {
			meta = v.definition.SharedValueMeta(dictionaries, value)
		}
		metadata[value] = meta
	}
	if field.DictionarySelection {
		sortAttributeValues(option.Values, stringValue, func(value string) *int {
			return metadata[value].Order
		})
	}
	option.ValueDetails = make([]VariantValue, 0, len(option.Values))
	for _, value := range option.Values {
		option.ValueDetails = append(option.ValueDetails, VariantValue{
			Value: value,
			Meta:  metadata[value].DisplayMeta(),
		})
	}
}

func stringValue(value string) string {
	return value
}

func sortAttributeValues[T any](
	values []T,
	valueOf func(T) string,
	orderOf func(T) *int,
) {
	orders := make(map[string]valueOrder, len(values))
	for _, item := range values {
		value := valueOf(item)
		first, last, numeric := numberRange(value)
		orders[value] = valueOrder{
			position: orderOf(item),
			first:    first,
			last:     last,
			numeric:  numeric,
		}
	}
	slices.SortStableFunc(values, func(a, b T) int {
		return compareValueOrder(orders[valueOf(a)], orders[valueOf(b)])
	})
}

func compareValueOrder(a, b valueOrder) int {
	switch {
	case a.position != nil && b.position != nil && *a.position != *b.position:
		return cmp.Compare(*a.position, *b.position)
	case a.position != nil && b.position == nil:
		return -1
	case a.position == nil && b.position != nil:
		return 1
	case a.numeric && b.numeric:
		if first := cmp.Compare(a.first, b.first); first != 0 {
			return first
		}
		return cmp.Compare(a.last, b.last)
	case a.numeric:
		return -1
	case b.numeric:
		return 1
	}
	return 0
}

// numberRange разбирает одиночную маркировку и диапазоны с типографскими тире.
func numberRange(value string) (float64, float64, bool) {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '-' || r == '–' || r == '—'
	})
	if len(parts) == 0 || len(parts) > 2 {
		return 0, 0, false
	}
	first, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(parts[0]), ",", "."), 64)
	if err != nil {
		return 0, 0, false
	}
	last := first
	if len(parts) == 2 {
		last, err = strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(parts[1]), ",", "."), 64)
	}
	return first, last, err == nil && !math.IsInf(first, 0) && !math.IsInf(last, 0) && first <= last
}
