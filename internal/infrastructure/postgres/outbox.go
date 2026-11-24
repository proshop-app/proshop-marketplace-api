package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	insertTask = `
		INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
		VALUES ($1, $2, $3, $4, $6, $5)
		ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO UPDATE
		SET next_at = EXCLUDED.next_at
		WHERE EXCLUDED.next_at < outbox.next_at`

	// claimTasks берёт созревшие задачи с квотой на магазин и пропуском занятых строк.
	claimTasks = `
		WITH ordered_heads AS MATERIALIZED (
            SELECT DISTINCT ON (tenant_id, kind) id
            FROM outbox
            WHERE done_at IS NULL AND failed_at IS NULL AND kind = ANY($9::text[])
            ORDER BY tenant_id, kind, id
        ), due_tenants AS (
			SELECT DISTINCT tenant_id
			FROM outbox
			WHERE done_at IS NULL AND failed_at IS NULL AND next_at <= $1
				AND (cardinality($5::text[]) = 0 OR kind = ANY($5::text[]))
				AND NOT (kind = ANY($6::text[]))
		), candidates AS (
			SELECT c.id, c.next_at, c.position,
                CASE WHEN $8::text = '' THEN false ELSE t.tenant_id <= $8 END AS wrapped,
                CASE WHEN $8::text = '' THEN '' ELSE t.tenant_id END AS tenant_order
			FROM due_tenants t
			CROSS JOIN LATERAL (
				SELECT id, next_at, row_number() OVER (ORDER BY next_at, id) AS position
				FROM outbox
				WHERE tenant_id = t.tenant_id AND done_at IS NULL AND failed_at IS NULL AND next_at <= $1
				AND (cardinality($5::text[]) = 0 OR kind = ANY($5::text[]))
				AND NOT (kind = ANY($6::text[]))
                AND (NOT (kind = ANY($9::text[])) OR id IN (SELECT id FROM ordered_heads))
				ORDER BY next_at, id
				LIMIT COALESCE(($7::jsonb ->> t.tenant_id)::integer, $3)
			) c
			ORDER BY c.position, wrapped, tenant_order, c.next_at, c.id
			LIMIT $2
		), locked AS (
            SELECT selected.id
            FROM candidates c
            CROSS JOIN LATERAL (
                SELECT o.id, o.done_at, o.failed_at, o.next_at
                FROM outbox o
                WHERE o.id = c.id
                -- Не переносить проверку готовности внутрь поиска: иначе неверная оценка выбирает due-индекс.
                OFFSET 0
                FOR UPDATE SKIP LOCKED
            ) selected
            WHERE selected.done_at IS NULL AND selected.failed_at IS NULL AND selected.next_at <= $1
		), updated AS (
			UPDATE outbox
			SET attempts = outbox.attempts + 1,
				next_at = $4
			FROM locked
			WHERE outbox.id = locked.id
			RETURNING outbox.id, outbox.kind, outbox.tenant_id, outbox.payload, outbox.attempts, outbox.created_at,
				COALESCE(outbox.unknown_failures, 0) AS unknown_failures
		)
		SELECT u.* FROM updated u JOIN candidates c ON c.id = u.id
		ORDER BY c.position, c.wrapped, c.tenant_order, c.next_at, c.id`

	completeTask = `
		UPDATE outbox
		SET done_at = $3,
			last_error = NULL
		WHERE id = $1 AND attempts = $2 AND attempts > 0 AND done_at IS NULL AND failed_at IS NULL`

	retryTask = `
		UPDATE outbox
		SET next_at = $3,
			last_error = $4
		WHERE id = $1 AND attempts = $2 AND attempts > 0 AND done_at IS NULL AND failed_at IS NULL`
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

// Claim берёт созревшие задачи: не больше PerTenant на магазин, всего не больше
// Limit, занятые другим процессом пропускаются. Взятие продлевает срок до
// конца аренды и считает попытку.
func (q *TaskQueue) Claim(ctx context.Context, request ports.ClaimRequest) ([]ports.Task, error) {
	var tasks []ports.Task

	err := q.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		var err error
		tasks, err = claimOutboxTasks(ctx, db, request)
		return err
	})

	return tasks, err
}

func claimOutboxTasks(ctx context.Context, db *gorm.DB, request ports.ClaimRequest) ([]ports.Task, error) {
	slots, err := json.Marshal(request.TenantSlots)
	if err != nil {
		return nil, err
	}
	rows, err := rawSQL(
		ctx,
		db,
		claimTasks,
		request.Now,
		request.Limit,
		request.PerTenant,
		request.LeaseUntil,
		append([]string{}, request.Kinds...),
		append([]string{}, request.ExcludeKinds...),
		string(slots),
		string(request.AfterTenant),
		append([]string{}, request.OrderedKinds...),
	).Rows()
	if err != nil {
		return nil, fmt.Errorf("взятие задач: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

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
			&task.UnknownFailures,
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

	return tasks, nil
}

// Complete отмечает задачу выполненной.
func (q *TaskQueue) Complete(ctx context.Context, id int64, attempts int, at time.Time) error {
	return q.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		affected, err := execSQL(ctx, db, completeTask, id, attempts, at)
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
