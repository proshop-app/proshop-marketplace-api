package catalog

import (
	"reflect"
	"time"
)

// IdenticalProjectionContent отличает повторную доставку от изменения карточки и коммерческих условий.
// Ключ модели вычисляет индексатор, поисковые выражения — таксономия; магазин их не определяет.
func IdenticalProjectionContent(previous, current ProductState) bool {
	return reflect.DeepEqual(projectionContent(previous), projectionContent(current))
}

func projectionContent(state ProductState) ProductState {
	state.Revision = 0
	state.ProjectedAt = time.Time{}
	state.Version = 0
	state.UpdatedAt = time.Time{}
	state.Card.ModelKey = ""
	state.Card.ModelKeySource = ""
	state.Card.Publication.Version = 0
	state.Card.Publication.AttributeSearchTerms = nil
	return state
}
