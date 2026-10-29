package ports

import (
	"context"
	"errors"
)

// ErrRebuildBusy — каталог уже перестраивает другой процесс.
var ErrRebuildBusy = errors.New("каталог уже перестраивает другой процесс")

// IndexRebuildLock удерживает исключительное владение до окончания callback.
// При потере соединения операция прерывается; публикуется только завершённая сборка.
type IndexRebuildLock interface {
	WithLock(context.Context, func(context.Context) error) error
}
