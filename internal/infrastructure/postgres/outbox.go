package postgres

import (
	"context"
	"fmt"
	"slices"
	"time"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	mergePendingTasks = `ON CONFLICT (dedup_key) WHERE attempts = 0 DO UPDATE
		SET next_at = EXCLUDED.next_at WHERE EXCLUDED.next_at < outbox.next_at`

	insertTask = `
		INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
		SELECT $1, $2, $3, $4, $6, $5
		WHERE NOT marketplace_store_task($1) OR EXISTS (
			SELECT 1 FROM marketplace_open_tenants(ARRAY[$2::text], false))
		` + mergePendingTasks

	// completeTask удаляет выполненную задачу: завершённые не копятся и не ждут чистки обходом.
	completeTask = `
		DELETE FROM outbox
		WHERE id = $1 AND attempts = $2 AND attempts > 0 AND failed_at IS NULL RETURNING kind`

	retryTask = `
		UPDATE outbox
		SET next_at = $3,
			last_error = $4
		WHERE id = $1 AND attempts = $2 AND attempts > 0 AND failed_at IS NULL RETURNING kind`
)

// Outbox — запись задач в транзакции изменения.
type Outbox struct {
	db   *gorm.DB
	wake func(string, time.Time)
}

// TaskQueue — разбор outbox вне транзакций изменения.
type TaskQueue struct {
	source *connectionSource
	wake   ports.WorkSignal
}

// Enqueue ставит задачу; невзятая задача с тем же ключом склеивает повтор.
func (o *Outbox) Enqueue(ctx context.Context, task ports.NewTask) error {
	next := task.AvailableAt
	if next.IsZero() {
		next = task.At
	}
	var dedup any
	if task.DedupKey != "" {
		dedup = task.DedupKey
	}

	affected, err := execSQL(
		ctx,
		o.db,
		insertTask,
		task.Kind,
		string(task.TenantID),
		dedup,
		string(task.Payload),
		task.At,
		next,
	)
	if err != nil {
		return fmt.Errorf("задача %s: %w", task.Kind, err)
	}

	if affected > 0 && o.wake != nil {
		o.wake(task.Kind, next)
	}
	return nil
}

// NewTaskQueue — очередь задач поверх пула.
func NewTaskQueue(pool *ClassPool) *TaskQueue {
	return &TaskQueue{source: newConnectionSource(pool)}
}

// WithWorkSignal сообщает об изменении сроков после отметки результата.
func (q *TaskQueue) WithWorkSignal(wake ports.WorkSignal) *TaskQueue {
	q.wake = wake
	return q
}

// Complete удаляет выполненную задачу; время итога очереди не нужно — его пишут метрики.
func (q *TaskQueue) Complete(ctx context.Context, id int64, attempts int, _ time.Time) error {
	kind, err := q.markTask(ctx, completeTask, id, attempts)
	if err == nil && q.wake != nil && slices.Contains(OrderedTaskKinds, kind) {
		q.wake.Notify(ports.WorkHints{kind: {}})
	}
	return err
}

// Retry откладывает задачу с причиной.
func (q *TaskQueue) Retry(ctx context.Context, id int64, attempts int, nextAt time.Time, reason string) error {
	kind, err := q.markTask(ctx, retryTask, id, attempts, nextAt, reason)
	if err == nil && q.wake != nil {
		q.wake.Notify(ports.WorkHints{kind: nextAt})
	}
	return err
}

func (q *TaskQueue) markTask(ctx context.Context, query string, id int64, args ...any) (string, error) {
	var kind string
	err := q.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		err := rawSQL(ctx, db, query, append([]any{id}, args...)...).Row().Scan(&kind)
		if notFound(err) {
			return ports.ErrTaskClaimConflict
		}
		if err != nil {
			return fmt.Errorf("задача %d: %w", id, err)
		}
		return nil
	})
	return kind, err
}
