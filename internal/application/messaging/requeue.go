package messaging

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// Requeue повторяет исходное намерение, сохраняя payload, id и возрастающий токен аренды.
func (d *Dispatcher) Requeue(ctx context.Context, request ports.TaskRequeue) ([]int64, error) {
	if request.ID < 0 || request.After < 0 || (request.ID == 0) == (request.Kind == "") ||
		(request.ID > 0 && request.After != 0) || request.Limit < 1 || request.Limit > 100 {
		return nil, errs.New(errs.Invalid, "messaging.requeue_filter", "нужна задача или страница одного вида")
	}
	request.At = d.clock.Now()
	items, err := d.queue.Requeue(ctx, request)
	if err == nil && request.ID > 0 && len(items) == 0 {
		return nil, errs.New(errs.NotFound, "messaging.failure_not_found", "задача не находится в карантине")
	}
	return items, err
}
