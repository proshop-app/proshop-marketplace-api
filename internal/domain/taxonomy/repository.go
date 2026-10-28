package taxonomy

import "context"

// Repository — снимки таксономии в транзакции. Пустое хранилище имеет версию 0.
type Repository interface {
	LatestRelease(ctx context.Context) (*ReleaseRecord, error)
	RecordRelease(ctx context.Context, release Release, version int64, previous Definition) error
	MarkRollback(ctx context.Context, release int64, version int64) error
	// Current берёт разделяемую блокировку до конца транзакции.
	Current(ctx context.Context) (Definition, error)
	// Lock сериализует загрузку и приём карточек; вызывается до Current при загрузке.
	Lock(ctx context.Context) error
	Save(ctx context.Context, definition Definition, expected int64) error
}
