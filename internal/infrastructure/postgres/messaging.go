package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Outbox — запись задач в транзакции изменения.
type Outbox struct {
	q querier
}

// Enqueue ставит задачу; невзятая задача с тем же ключом склеивает повтор.
func (o *Outbox) Enqueue(ctx context.Context, task ports.NewTask) error {
	var dedup any
	if task.DedupKey != "" {
		dedup = task.DedupKey
	}

	_, err := o.q.Exec(ctx, `INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO NOTHING`,
		task.Kind, string(task.TenantID), dedup, string(task.Payload), task.At)
	if err != nil {
		return fmt.Errorf("задача %s: %w", task.Kind, err)
	}

	return nil
}

// TaskQueue — разбор outbox вне транзакций изменения.
type TaskQueue struct {
	pool *pgxpool.Pool
}

// NewTaskQueue — очередь задач поверх пула.
func NewTaskQueue(pool *pgxpool.Pool) *TaskQueue {
	return &TaskQueue{pool: pool}
}

// Claim берёт созревшие задачи: не больше PerTenant на магазин, всего не больше
// Limit, занятые другим процессом пропускаются. Взятие продлевает срок до
// конца аренды и считает попытку.
func (q *TaskQueue) Claim(ctx context.Context, request ports.ClaimRequest) ([]ports.Task, error) {
	rows, err := q.pool.Query(ctx, `
WITH due_tenants AS (
    SELECT DISTINCT tenant_id FROM outbox WHERE done_at IS NULL AND next_at <= $1
), candidates AS (
    SELECT c.id, c.next_at
    FROM due_tenants t
    CROSS JOIN LATERAL (
        SELECT id, next_at FROM outbox
        WHERE tenant_id = t.tenant_id AND done_at IS NULL AND next_at <= $1
        ORDER BY next_at, id
        LIMIT $3
    ) c
    ORDER BY c.next_at, c.id
    LIMIT $2
), locked AS (
    SELECT o.id FROM outbox o
    JOIN candidates c ON c.id = o.id
    WHERE o.done_at IS NULL AND o.next_at <= $1
    FOR UPDATE OF o SKIP LOCKED
)
UPDATE outbox SET attempts = outbox.attempts + 1, next_at = $4
FROM locked
WHERE outbox.id = locked.id
RETURNING outbox.id, outbox.kind, outbox.tenant_id, outbox.payload, outbox.attempts, outbox.created_at`,
		request.Now, request.Limit, request.PerTenant, request.LeaseUntil)
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

		if err := rows.Scan(&task.ID, &task.Kind, &tenant, &task.Payload, &task.Attempts, &task.CreatedAt); err != nil {
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
	if _, err := q.pool.Exec(ctx, `UPDATE outbox SET done_at = $2, last_error = NULL WHERE id = $1`, id, at); err != nil {
		return fmt.Errorf("задача %d: %w", id, err)
	}

	return nil
}

// Retry откладывает задачу с причиной.
func (q *TaskQueue) Retry(ctx context.Context, id int64, nextAt time.Time, reason string) error {
	if _, err := q.pool.Exec(ctx, `UPDATE outbox SET next_at = $2, last_error = $3 WHERE id = $1 AND done_at IS NULL`,
		id, nextAt, reason); err != nil {
		return fmt.Errorf("задача %d: %w", id, err)
	}

	return nil
}

// Inbox — отметки обработанных событий.
type Inbox struct {
	q querier
}

// MarkProcessed отмечает событие; false — уже было.
func (i *Inbox) MarkProcessed(ctx context.Context, eventID string, at time.Time) (bool, error) {
	tag, err := i.q.Exec(ctx, `INSERT INTO processed_events (id, processed_at) VALUES ($1, $2) ON CONFLICT DO NOTHING`, eventID, at)
	if err != nil {
		return false, fmt.Errorf("отметка события %s: %w", eventID, err)
	}

	return tag.RowsAffected() == 1, nil
}

// Parked — отравленные сообщения.
type Parked struct {
	q querier
}

// Park сохраняет сообщение; повтор той же позиции ничего не меняет.
func (p *Parked) Park(ctx context.Context, event ports.ParkedEvent) error {
	headers, err := json.Marshal(event.Headers)
	if err != nil {
		return fmt.Errorf("заголовки сообщения: %w", err)
	}

	var tenant any
	if event.TenantID != "" {
		tenant = event.TenantID
	}

	_, err = p.q.Exec(ctx, `INSERT INTO parked_events
		(topic, kafka_partition, kafka_offset, message_key, headers, value, tenant_id, reason, error, attempts, parked_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (topic, kafka_partition, kafka_offset) DO NOTHING`,
		event.Topic, event.Partition, event.Offset, event.Key, string(headers), event.Value, tenant,
		event.Reason, event.Error, event.Attempts, event.ParkedAt)
	if err != nil {
		return fmt.Errorf("парковка %s/%d/%d: %w", event.Topic, event.Partition, event.Offset, err)
	}

	return nil
}

// Retention — чистка служебных записей пачками, чтобы не держать долгих блокировок.
type Retention struct {
	pool *pgxpool.Pool
}

// NewRetention — чистка поверх пула.
func NewRetention(pool *pgxpool.Pool) *Retention {
	return &Retention{pool: pool}
}

// Purge удаляет записи старше границ.
func (r *Retention) Purge(ctx context.Context, request ports.PurgeRequest) (ports.PurgeResult, error) {
	var (
		result ports.PurgeResult
		err    error
	)

	if result.Processed, err = r.purge(ctx, `DELETE FROM processed_events WHERE id IN
		(SELECT id FROM processed_events WHERE processed_at < $1 LIMIT $2)`, request.ProcessedBefore, request.Batch); err != nil {
		return result, err
	}
	if result.OutboxDone, err = r.purge(ctx, `DELETE FROM outbox WHERE id IN
		(SELECT id FROM outbox WHERE done_at < $1 LIMIT $2)`, request.OutboxDoneBefore, request.Batch); err != nil {
		return result, err
	}
	if result.Parked, err = r.purge(ctx, `DELETE FROM parked_events WHERE id IN
		(SELECT id FROM parked_events WHERE parked_at < $1 LIMIT $2)`, request.ParkedBefore, request.Batch); err != nil {
		return result, err
	}
	if result.Challenges, err = r.purge(ctx, `DELETE FROM phone_challenges WHERE id IN
		(SELECT id FROM phone_challenges WHERE expires_at < $1 LIMIT $2)`, request.ChallengesBefore, request.Batch); err != nil {
		return result, err
	}
	if result.Carts, err = r.purge(ctx, `DELETE FROM carts WHERE id IN
		(SELECT id FROM carts WHERE expires_at < $1 LIMIT $2)`, request.CartsBefore, request.Batch); err != nil {
		return result, err
	}

	return result, nil
}

func (r *Retention) purge(ctx context.Context, sql string, before time.Time, batch int) (int64, error) {
	var total int64

	for {
		tag, err := r.pool.Exec(ctx, sql, before, batch)
		if err != nil {
			return total, fmt.Errorf("чистка: %w", err)
		}

		total += tag.RowsAffected()
		if tag.RowsAffected() < int64(batch) {
			return total, nil
		}
	}
}
