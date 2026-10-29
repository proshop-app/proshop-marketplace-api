package fake

import (
	"context"
	"sync"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// IndexRebuildLock — исключительное владение пересборкой в памяти.
type IndexRebuildLock struct {
	mu sync.Mutex
}

// WithLock выполняет одну пересборку одновременно.
func (l *IndexRebuildLock) WithLock(ctx context.Context, run func(context.Context) error) error {
	if !l.mu.TryLock() {
		return ports.ErrRebuildBusy
	}
	defer l.mu.Unlock()

	return run(ctx)
}
