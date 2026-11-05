package ports

import "context"

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
	AttributeValues(context.Context, string, int, string) (AttributeValuesPage, error)
}
