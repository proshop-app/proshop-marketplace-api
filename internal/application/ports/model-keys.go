package ports

import "context"

// BrandResolver находит канонический идентификатор бренда.
type BrandResolver interface {
	Resolve(context.Context, string) (string, bool, error)
}

// DictionaryResolver разрешает значения любого словаря идентичности.
type DictionaryResolver interface {
	ResolveDictionary(context.Context, string, string) (string, bool, error)
}

// ModelKeyObserver учитывает ошибки разрешения бренда.
type ModelKeyObserver interface {
	ResolveFailed()
	Conflict()
}
