package ports

import (
	"context"
	"time"
)

// WithTimeout ограничивает операцию. Управляемые часы могут сами истекать сроки контекстов.
func WithTimeout(ctx context.Context, clock Clock, d time.Duration) (context.Context, context.CancelFunc) {
	if scheduler, ok := clock.(interface {
		WithTimeout(context.Context, time.Duration) (context.Context, context.CancelFunc)
	}); ok {
		return scheduler.WithTimeout(ctx, d)
	}
	return context.WithTimeout(ctx, d)
}
