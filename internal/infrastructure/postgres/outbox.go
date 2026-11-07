package postgres

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	insertTask = `
		INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO UPDATE
		SET next_at = LEAST(outbox.next_at, EXCLUDED.next_at),
			payload = CASE WHEN $6 THEN EXCLUDED.payload ELSE outbox.payload END
		WHERE $6 OR EXCLUDED.next_at < outbox.next_at`

	// claimTasks берёт созревшие задачи с квотой на магазин и пропуском занятых строк.
	claimTasks = `
		WITH due_tenants AS (
			SELECT DISTINCT tenant_id
			FROM outbox
			WHERE done_at IS NULL AND next_at <= $1
		), candidates AS (
			SELECT c.id, c.next_at
			FROM due_tenants t
			CROSS JOIN LATERAL (
				SELECT id, next_at
				FROM outbox
				WHERE tenant_id = t.tenant_id AND done_at IS NULL AND next_at <= $1
				ORDER BY next_at, id
				LIMIT $3
			) c
			ORDER BY c.next_at, c.id
			LIMIT $2
		), locked AS (
			SELECT o.id
			FROM outbox o
			JOIN candidates c ON c.id = o.id
			WHERE o.done_at IS NULL AND o.next_at <= $1
			FOR UPDATE OF o SKIP LOCKED
		)
		UPDATE outbox
		SET attempts = outbox.attempts + 1,
			next_at = $4
		FROM locked
		WHERE outbox.id = locked.id
		RETURNING outbox.id, outbox.kind, outbox.tenant_id, outbox.payload, outbox.attempts, outbox.created_at`

	completeTask = `
		UPDATE outbox
		SET done_at = $2,
			last_error = NULL
		WHERE id = $1`

	retryTask = `
		UPDATE outbox
		SET next_at = $2,
			last_error = $3
		WHERE id = $1 AND done_at IS NULL`
)

// Outbox — запись задач в транзакции изменения.
type Outbox struct {
	q querier
}

// TaskQueue — разбор outbox вне транзакций изменения.
type TaskQueue struct {
	pool *pgxpool.Pool
}

// Enqueue ставит задачу; невзятая задача с тем же ключом склеивает повтор.
func (o *Outbox) Enqueue(ctx context.Context, task ports.NewTask) error {
	var dedup any
	if task.DedupKey != "" {
		dedup = task.DedupKey
	}

	_, err := o.q.Exec(
		ctx,
		insertTask,
		task.Kind,
		string(task.TenantID),
		dedup,
		string(task.Payload),
		task.At,
		task.ReplacePayload,
	)
	if err != nil {
		return fmt.Errorf("задача %s: %w", task.Kind, err)
	}

	return nil
}

// NewTaskQueue — очередь задач поверх пула.
func NewTaskQueue(pool *pgxpool.Pool) *TaskQueue {
	return &TaskQueue{pool: pool}
}

// Claim берёт созревшие задачи: не больше PerTenant на магазин, всего не больше
// Limit, занятые другим процессом пропускаются. Взятие продлевает срок до
// конца аренды и считает попытку.
func (q *TaskQueue) Claim(ctx context.Context, request ports.ClaimRequest) ([]ports.Task, error) {
	rows, err := q.pool.Query(
		ctx,
		claimTasks,
		request.Now,
		request.Limit,
		request.PerTenant,
		request.LeaseUntil,
	)
	if err != nil {
		return nil, fmt.Errorf("взятие задач: %w", err)
	}
	defer rows.Close()

	var tasks []ports.Task

	for rows.Next() {
		var (
			task   ports.Task
			tenant string
		)

		err := rows.Scan(
			&task.ID,
			&task.Kind,
			&tenant,
			&task.Payload,
			&task.Attempts,
			&task.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("взятие задач: %w", err)
		}

		task.TenantID = ids.TenantID(tenant)
		tasks = append(tasks, task)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("взятие задач: %w", err)
	}

	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })

	return tasks, nil
}

// Complete отмечает задачу выполненной.
func (q *TaskQueue) Complete(ctx context.Context, id int64, at time.Time) error {
	if _, err := q.pool.Exec(ctx, completeTask, id, at); err != nil {
		return fmt.Errorf("задача %d: %w", id, err)
	}

	return nil
}

// Retry откладывает задачу с причиной.
func (q *TaskQueue) Retry(ctx context.Context, id int64, nextAt time.Time, reason string) error {
	if _, err := q.pool.Exec(ctx, retryTask, id, nextAt, reason); err != nil {
		return fmt.Errorf("задача %d: %w", id, err)
	}

	return nil
}
