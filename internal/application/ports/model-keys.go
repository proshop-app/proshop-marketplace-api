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

// DictionaryBatchResolver разрешает уникальные значения одного словаря без нечёткого поиска.
type DictionaryBatchResolver interface {
	ResolveDictionaryBatch(context.Context, string, []string) (map[string]string, error)
}
