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
		ON CONFLICT (dedup_key) WHERE attempts = 0 DO UPDATE
		SET next_at = EXCLUDED.next_at
		WHERE EXCLUDED.next_at < outbox.next_at`

	// claimTasks берёт созревшие задачи с квотой на магазин и пропуском занятых строк.
	// Очередь читается прыжками по outbox_lane_due_idx (вид, магазин, срок, id): один спуск
	// по индексу на полосу «вид × магазин», а не проход по всем задачам. lanes идёт по
	// магазинам от AfterTenant по кругу и останавливается на Limit готовых магазинах вида;
	// без AfterTenant порядок — по возрасту, и обходятся все полосы видов запроса.
	// Упорядоченный вид отдаёт только старейшую незавершённую задачу полосы, включая аренды
	// и повторы. Квота магазина не больше Limit − магазинов + 1: каждый выбранный магазин
	// даёт хотя бы одну задачу в первом круге, остальное не попадёт в пачку.
	claimTasks = `
		WITH RECURSIVE queued_kinds(kind) AS (
			(SELECT kind FROM outbox WHERE failed_at IS NULL ORDER BY kind LIMIT 1)
			UNION ALL
			SELECT following.kind
			FROM queued_kinds q
			CROSS JOIN LATERAL (
				SELECT kind FROM outbox WHERE failed_at IS NULL AND kind > q.kind ORDER BY kind LIMIT 1
			) following
		), kinds AS (
			SELECT kind FROM unnest($5::text[]) AS kind WHERE cardinality($5::text[]) > 0
			UNION
			SELECT kind FROM queued_kinds WHERE cardinality($5::text[]) = 0
		), lanes(kind, tenant_id, next_at, id, wrapped, ready) AS (
			SELECT head.kind, head.tenant_id, head.next_at, head.id, head.wrapped, (head.next_at <= $1)::integer
			FROM kinds k
			CROSS JOIN LATERAL (
				-- Общие задачи площадки идут с пустым магазином: без AfterTenant обход начинается с начала вида.
				SELECT * FROM (
					(SELECT kind, tenant_id, next_at, id, false AS wrapped
					 FROM outbox
					 WHERE $8 = '' AND kind = k.kind AND failed_at IS NULL
					 ORDER BY kind, tenant_id, next_at, id
					 LIMIT 1)
					UNION ALL
					(SELECT kind, tenant_id, next_at, id, false
					 FROM outbox
					 WHERE kind = k.kind AND tenant_id > $8 AND failed_at IS NULL
					 ORDER BY kind, tenant_id, next_at, id
					 LIMIT 1)
					UNION ALL
					(SELECT kind, tenant_id, next_at, id, true
					 FROM outbox
					 WHERE $8 <> '' AND kind = k.kind AND failed_at IS NULL
					 ORDER BY kind, tenant_id, next_at, id
					 LIMIT 1)
				) first
				LIMIT 1
			) head
			WHERE NOT (k.kind = ANY($6::text[]))
			UNION ALL
			SELECT head.kind, head.tenant_id, head.next_at, head.id, head.wrapped,
				l.ready + (head.next_at <= $1)::integer
			FROM lanes l
			CROSS JOIN LATERAL (
				SELECT * FROM (
					(SELECT kind, tenant_id, next_at, id, l.wrapped
					 FROM outbox
					 WHERE kind = l.kind AND tenant_id > l.tenant_id AND failed_at IS NULL
					 ORDER BY kind, tenant_id, next_at, id
					 LIMIT 1)
					UNION ALL
					(SELECT kind, tenant_id, next_at, id, true
					 FROM outbox
					 WHERE NOT l.wrapped AND $8 <> '' AND kind = l.kind AND failed_at IS NULL
					 ORDER BY kind, tenant_id, next_at, id
					 LIMIT 1)
				) following
				LIMIT 1
			) head
			WHERE ($8 = '' OR l.ready < $2) AND NOT (head.wrapped AND head.tenant_id > $8)
		), ready AS MATERIALIZED (
			SELECT l.kind, l.tenant_id, head.next_at, head.id, l.kind = ANY($9::text[]) AS ordered
			FROM lanes l
			CROSS JOIN LATERAL (
				SELECT l.next_at, l.id WHERE NOT (l.kind = ANY($9::text[]))
				UNION ALL
				(SELECT next_at, id
				 FROM outbox
				 WHERE l.kind = ANY($9::text[]) AND kind = l.kind AND tenant_id = l.tenant_id AND failed_at IS NULL
				 ORDER BY id
				 LIMIT 1)
			) head
			WHERE l.next_at <= $1 AND head.next_at <= $1 AND COALESCE(($7::jsonb ->> l.tenant_id)::integer, $3) > 0
		), chosen AS MATERIALIZED (
			SELECT tenant_id, wrapped, tenant_order
			FROM (
				SELECT DISTINCT ON (tenant_id) tenant_id, next_at, id,
					CASE WHEN $8::text = '' THEN false ELSE tenant_id <= $8 END AS wrapped,
					CASE WHEN $8::text = '' THEN '' ELSE tenant_id END AS tenant_order
				FROM ready
				ORDER BY tenant_id, next_at, id
			) tenants
			ORDER BY wrapped, tenant_order, next_at, id
			LIMIT $2
		), candidates AS MATERIALIZED (
			SELECT picked.id, picked.next_at, picked.position, c.wrapped, c.tenant_order
			FROM chosen c
			CROSS JOIN LATERAL (
				SELECT least(COALESCE(($7::jsonb ->> c.tenant_id)::integer, $3), $2 - (SELECT count(*) FROM chosen) + 1)
					AS quota
			) q
			CROSS JOIN LATERAL (
				SELECT task.id, task.next_at, row_number() OVER (ORDER BY task.next_at, task.id) AS position
				FROM ready r
				CROSS JOIN LATERAL (
					SELECT r.id, r.next_at WHERE r.ordered
					UNION ALL
					(SELECT id, next_at
					 FROM outbox
					 WHERE NOT r.ordered AND kind = r.kind AND tenant_id = r.tenant_id
						AND failed_at IS NULL AND next_at <= $1
					 ORDER BY next_at, id
					 LIMIT q.quota)
				) task
				WHERE r.tenant_id = c.tenant_id
				ORDER BY task.next_at, task.id
				LIMIT q.quota
			) picked
			ORDER BY picked.position, c.wrapped, c.tenant_order, picked.next_at, picked.id
			LIMIT $2
		), locked AS (
			SELECT selected.id
			FROM candidates c
			CROSS JOIN LATERAL (
				SELECT o.id, o.failed_at, o.next_at
				FROM outbox o
				WHERE o.id = c.id
				-- Не переносить проверку готовности внутрь поиска: иначе неверная оценка выбирает другой индекс.
				OFFSET 0
				FOR UPDATE SKIP LOCKED
			) selected
			WHERE selected.failed_at IS NULL AND selected.next_at <= $1
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
