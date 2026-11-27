package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	insertTasks = `
		INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
		SELECT kind, tenant_id, NULLIF(dedup_key, ''), payload, next_at, created_at
		FROM jsonb_to_recordset($1::jsonb)
		AS t(kind text, tenant_id text, dedup_key text, payload jsonb, next_at timestamptz, created_at timestamptz)
		` + mergePendingTasks

	// resolveTasks удаляет выполненные задачи и сохраняет повторы и карантин одним оператором.
	resolveTasks = `
		WITH results AS (
			SELECT * FROM jsonb_to_recordset($1::jsonb)
			AS r(id bigint, attempts integer, at timestamptz, next_at timestamptz, error text, failure_reason text,
				unknown_failures integer, pause_key text, pause_until timestamptz)
		), done AS (
			DELETE FROM outbox o
			USING results r
			WHERE o.id = r.id AND o.attempts = r.attempts AND o.attempts > 0 AND o.failed_at IS NULL
				AND r.next_at IS NULL AND r.failure_reason = ''
			RETURNING o.id, o.kind
		), kept AS (
			UPDATE outbox o
			SET unknown_failures = NULLIF(r.unknown_failures, 0),
				next_at = COALESCE(r.next_at, o.next_at),
				failed_at = CASE WHEN r.failure_reason <> '' THEN r.at ELSE NULL END,
				failure_reason = NULLIF(r.failure_reason, ''),
				last_error = NULLIF(r.error, '')
			FROM results r
			WHERE o.id = r.id AND o.attempts = r.attempts AND o.attempts > 0 AND o.failed_at IS NULL
				AND (r.next_at IS NOT NULL OR r.failure_reason <> '')
			RETURNING o.id, o.kind, o.failed_at, o.next_at
		), paused AS (
			INSERT INTO outbox_lane_pauses(lane, resume_at)
			SELECT r.pause_key, max(r.pause_until)
			FROM kept k JOIN results r ON r.id = k.id
			WHERE r.pause_key <> '' AND r.pause_until IS NOT NULL
			GROUP BY r.pause_key
			ORDER BY r.pause_key
			ON CONFLICT (lane) DO UPDATE
			SET resume_at = EXCLUDED.resume_at
			WHERE outbox_lane_pauses.resume_at < EXCLUDED.resume_at
		)
		SELECT (SELECT count(*) FROM done) + (SELECT count(*) FROM kept),
			(SELECT json_object_agg(kind, next_at) FROM (
				SELECT kind, CASE WHEN bool_or(next_at IS NULL) THEN NULL ELSE min(next_at) END AS next_at
				FROM (
					SELECT kind, CASE WHEN failed_at IS NULL THEN next_at END AS next_at FROM kept
					WHERE failed_at IS NULL OR kind IN ` + orderedLaneKinds + `
					UNION ALL SELECT kind, NULL FROM done WHERE kind IN ` + orderedLaneKinds + `
				) signals GROUP BY kind
			) grouped)`

	// selectReadyLanes обходит полосы «вид × магазин» прыжками по outbox_lane_due_idx: число спусков
	// по индексу равно числу полос, а не задач. Первая запись полосы — её ближайший срок.
	selectReadyLanes = `
		WITH RECURSIVE lanes(kind, tenant_id, next_at) AS (
			(SELECT kind, tenant_id, next_at
			 FROM outbox
			 WHERE failed_at IS NULL
			 ORDER BY kind, tenant_id, next_at, id
			 LIMIT 1)
			UNION ALL
			SELECT head.kind, head.tenant_id, head.next_at
			FROM lanes l
			CROSS JOIN LATERAL (
				SELECT kind, tenant_id, next_at
				FROM outbox
				WHERE (kind, tenant_id) > (l.kind, l.tenant_id) AND failed_at IS NULL
				ORDER BY kind, tenant_id, next_at, id
				LIMIT 1
			) head
		)
		SELECT kind, count(*) FILTER (WHERE next_at <= $1), COALESCE(min(next_at) FILTER (WHERE next_at <= $1), $1)
		FROM lanes
		GROUP BY kind`
)

type newTaskRow struct {
	Kind      string          `json:"kind"`
	TenantID  string          `json:"tenant_id"`
	DedupKey  string          `json:"dedup_key"`
	Payload   json.RawMessage `json:"payload"`
	NextAt    time.Time       `json:"next_at"`
	CreatedAt time.Time       `json:"created_at"`
}

type taskResolution struct {
	PauseKey        string     `json:"pause_key,omitempty"`
	PauseUntil      *time.Time `json:"pause_until,omitempty"`
	UnknownFailures int        `json:"unknown_failures"`
	FailureReason   string     `json:"failure_reason"`
	ID              int64      `json:"id"`
	Attempts        int        `json:"attempts"`
	At              time.Time  `json:"at"`
	NextAt          *time.Time `json:"next_at"`
	Error           string     `json:"error"`
}

// EnqueueBatch добавляет позиции в транзакции вызывающего сценария.
func (o *Outbox) EnqueueBatch(ctx context.Context, tasks []ports.NewTask) error {
	switch len(tasks) {
	case 0:
		return nil
	case 1:
		return o.Enqueue(ctx, tasks[0])
	}
	rows := make([]newTaskRow, 0, len(tasks))
	for _, task := range tasks {
		next := task.AvailableAt
		if next.IsZero() {
			next = task.At
		}
		rows = append(rows, newTaskRow{
			Kind:      task.Kind,
			TenantID:  string(task.TenantID),
			DedupKey:  task.DedupKey,
			Payload:   task.Payload,
			NextAt:    next,
			CreatedAt: task.At,
		})
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("пачка задач: %w", err)
	}
	affected, err := execSQL(ctx, o.db, insertTasks, string(encoded))
	if err != nil {
		return fmt.Errorf("постановка пачки задач: %w", err)
	}
	if affected > 0 && o.wake != nil {
		for _, row := range rows {
			o.wake(row.Kind, row.NextAt)
		}
	}
	return nil
}

// Resolve проверяет токены всей пачки в транзакции: конфликт откатывает все отметки.
func (q *TaskQueue) Resolve(ctx context.Context, results []ports.TaskResolution) error {
	if len(results) == 0 {
		return nil
	}
	rows := make([]taskResolution, 0, len(results))
	for _, result := range results {
		rows = append(rows, taskResolution(result))
	}
	body, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("результаты задач: %w", err)
	}
	var kinds []byte
	err = q.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		apply := func(tx *gorm.DB) error {
			var count int64
			if err := rawSQL(ctx, tx, resolveTasks, string(body)).Row().Scan(&count, &kinds); err != nil {
				return fmt.Errorf("отметка задач: %w", err)
			}
			if count != int64(len(results)) {
				return ports.ErrTaskClaimConflict
			}
			return nil
		}
		if len(results) == 1 {
			return apply(db)
		}
		return db.Transaction(apply)
	})
	if err == nil && q.wake != nil {
		var changed ports.WorkHints
		if json.Unmarshal(kinds, &changed) == nil {
			q.wake.Notify(changed)
		}
	}
	return err
}

// ReadyLanes считает по видам магазины с созревшей задачей и срок старейшей из них; аренды
// и отложенные повторы не созрели.
func (q *TaskQueue) ReadyLanes(ctx context.Context, now time.Time) ([]ports.ReadyLane, error) {
	lanes := []ports.ReadyLane{}
	err := q.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		rows, err := rawSQL(ctx, db, selectReadyLanes, now).Rows()
		if err != nil {
			return err
		}
		defer func() {
			_ = rows.Close()
		}()
		for rows.Next() {
			var lane ports.ReadyLane
			if err := rows.Scan(&lane.Kind, &lane.Tenants, &lane.Oldest); err != nil {
				return err
			}
			lanes = append(lanes, lane)
		}
		return rows.Err()
	})
	return lanes, err
}
