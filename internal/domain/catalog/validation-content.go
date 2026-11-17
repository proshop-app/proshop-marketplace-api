package catalog

import "reflect"

// SameValidationContent сравнивает только входы проверки схемы и автоматической модерации.
// Цена Marketplace.Price не участвует в taxonomy.Check; GTIN/ISBN входят в Attributes.
func SameValidationContent(previous, current ProductState) bool {
	if previous.Deleted != current.Deleted || previous.Card.Name != current.Card.Name ||
		previous.Card.Description != current.Card.Description || previous.Card.Image != current.Card.Image {
		return false
	}
	before, after := previous.Card.Marketplace, current.Card.Marketplace
	if before == nil || after == nil {
		return before == after
	}
	return before.CategoryID == after.CategoryID && before.Published == after.Published &&
		reflect.DeepEqual(before.Attributes, after.Attributes)
}
