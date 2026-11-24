package catalog

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// batchOutbox копит задачи пачки до одной вставки и склеивает задачи с одним ключом так же, как
// склеивает невзятые задачи база: остаётся первая, срок — ближайший.
type batchOutbox struct {
	tasks []ports.NewTask
	keys  map[string]int
}

func (o *batchOutbox) Enqueue(_ context.Context, task ports.NewTask) error {
	o.add(task)
	return nil
}

func (o *batchOutbox) EnqueueBatch(_ context.Context, tasks []ports.NewTask) error {
	for _, task := range tasks {
		o.add(task)
	}
	return nil
}

func (o *batchOutbox) add(task ports.NewTask) {
	if task.DedupKey == "" {
		o.tasks = append(o.tasks, task)
		return
	}
	position, ok := o.keys[task.DedupKey]
	if !ok {
		o.keys[task.DedupKey] = len(o.tasks)
		o.tasks = append(o.tasks, task)
		return
	}
	first := &o.tasks[position]
	if due := taskDue(task); due.Before(taskDue(*first)) {
		first.AvailableAt = due
	}
}

func taskDue(task ports.NewTask) time.Time {
	if task.AvailableAt.IsZero() {
		return task.At
	}
	return task.AvailableAt
}
