package postgres

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	mergePendingTasks = `ON CONFLICT (dedup_key) WHERE attempts = 0 DO UPDATE
		SET next_at = EXCLUDED.next_at WHERE EXCLUDED.next_at < outbox.next_at`

	insertTask = `
		INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
		VALUES ($1, $2, $3, $4, $6, $5)
		` + mergePendingTasks

	// completeTask удаляет выполненную задачу: завершённые не копятся и не ждут чистки обходом.
	completeTask = `
		DELETE FROM outbox
		WHERE id = $1 AND attempts = $2 AND attempts > 0 AND failed_at IS NULL`

	retryTask = `
		UPDATE outbox
		SET next_at = $3,
			last_error = $4
		WHERE id = $1 AND attempts = $2 AND attempts > 0 AND failed_at IS NULL`
)

// Outbox — запись задач в транзакции изменения.
type Outbox struct {
	db *gorm.DB
}

// TaskQueue — разбор outbox вне транзакций изменения.
type TaskQueue struct {
	source *connectionSource
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

	_, err := execSQL(
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

	return nil
}

// NewTaskQueue — очередь задач поверх пула.
func NewTaskQueue(pool *ClassPool) *TaskQueue {
	return &TaskQueue{source: newConnectionSource(pool)}
}

// Complete удаляет выполненную задачу; время итога очереди не нужно — его пишут метрики.
func (q *TaskQueue) Complete(ctx context.Context, id int64, attempts int, _ time.Time) error {
	return q.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		affected, err := execSQL(ctx, db, completeTask, id, attempts)
		if err != nil {
			return fmt.Errorf("задача %d: %w", id, err)
		}
		if affected == 0 {
			return ports.ErrTaskClaimConflict
		}

		return nil
	})
}

// Retry откладывает задачу с причиной.
func (q *TaskQueue) Retry(ctx context.Context, id int64, attempts int, nextAt time.Time, reason string) error {
	return q.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		affected, err := execSQL(ctx, db, retryTask, id, attempts, nextAt, reason)
		if err != nil {
			return fmt.Errorf("задача %d: %w", id, err)
		}
		if affected == 0 {
			return ports.ErrTaskClaimConflict
		}

		return nil
	})
}
