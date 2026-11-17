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
// DictionaryBindings задают подсказки и не участвуют в Check.
func AffectedCategories(previous, next Definition) []string {
	before, after := previous.Checker(), next.Checker()
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
