package taxonomy

import (
	"reflect"
	"slices"
	"time"
)

// Revalidation хранит прогресс одной категории. Курсор фиксируется вместе с товарами и outbox.
type Revalidation struct {
	Lease         int64
	AvailableAt   time.Time
	Version       int64
	Category      string
	TenantCursor  string
	ProductCursor string
	Processed     int64
	Done          bool
}

// AffectedCategories сравнивает тот же контекст, который исполняет Checker.Check.
// Для выбора правила определяют пул, а из метаданных в проекцию попадает покрытие.
func AffectedCategories(previous, next Definition) []string {
	before, after := previous.revalidationDefinition().Checker(), next.revalidationDefinition().Checker()
	changed := []string{}
	for id, old := range before.categories {
		current, exists := after.categories[id]
		if !exists || !reflect.DeepEqual(old, current) {
			changed = append(changed, id)
		}
	}
	for id := range after.categories {
		if _, exists := before.categories[id]; !exists {
			changed = append(changed, id)
		}
	}
	slices.Sort(changed)
	return changed
}

func (d Definition) revalidationDefinition() Definition {
	d.Attributes = slices.Clone(d.Attributes)
	for i := range d.Attributes {
		field := &d.Attributes[i]
		field.DisplayName = ""
		field.Hint = ""
		field.ValuesMeta = nil
	}
	if len(d.SelectionValues) > 0 {
		pools := make(map[string]DictionaryValues, len(d.SelectionValues))
		for code, pool := range d.SelectionValues {
			values := make(map[string]ValueMeta, len(pool.ValuesMeta))
			for value, meta := range pool.ValuesMeta {
				values[value] = ValueMeta{Covers: meta.Covers}
			}
			pool.ValuesMeta = values
			pools[code] = pool
		}
		d.SelectionValues = pools
	}
	return d
}
