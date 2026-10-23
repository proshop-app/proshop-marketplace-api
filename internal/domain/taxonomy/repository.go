package taxonomy

import "context"

// Repository — снимки таксономии в транзакции. Пустое хранилище имеет версию 0.
type Repository interface {
	// Current берёт разделяемую блокировку до конца транзакции.
	Current(ctx context.Context) (Definition, error)
	// Lock сериализует загрузку и приём карточек; вызывается до Current при загрузке.
	Lock(ctx context.Context) error
	Save(ctx context.Context, definition Definition, expected int64) error
}
