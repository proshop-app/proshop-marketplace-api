package ports

import "context"

// MaxCandidateCategories — ограничение каждого фильтра разделов в контракте поиска.
const MaxCandidateCategories = 32

// AttributeValuesQuery сохраняется между страницами, меняется только After.
type AttributeValuesQuery struct {
	Attribute         string
	Categories        []string
	ExcludeCategories []string
	Size              int
	After             string
}

// AttributeValue — наблюдение значения в текущем поколении каталога.
type AttributeValue struct {
	Value     string `json:"value"`
	Documents int64  `json:"documents"`
	Sellers   int64  `json:"sellers"`
}

// AttributeValuesPage — курсорная страница значений.
type AttributeValuesPage struct {
	Generation string           `json:"generation"`
	Items      []AttributeValue `json:"items"`
	After      string           `json:"after"`
}

// ReferenceCandidates — чтение значений каталога отдельным служебным токеном.
type ReferenceCandidates interface {
	AttributeValues(context.Context, AttributeValuesQuery) (AttributeValuesPage, error)
}
