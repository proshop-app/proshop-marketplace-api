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
	resolveTasks = `
		WITH results AS (
			SELECT * FROM jsonb_to_recordset($1::jsonb)
			AS r(id bigint, attempts integer, at timestamptz, next_at timestamptz, error text, failure_reason text)
		)
		UPDATE outbox o
		SET done_at = CASE WHEN r.next_at IS NULL AND r.failure_reason = '' THEN r.at ELSE NULL END,
			next_at = COALESCE(r.next_at, o.next_at),
			failed_at = CASE WHEN r.failure_reason <> '' THEN r.at ELSE NULL END,
			failure_reason = NULLIF(r.failure_reason, ''),
			last_error = NULLIF(r.error, '')
		FROM results r
		WHERE o.id = r.id AND o.attempts = r.attempts AND o.attempts > 0 AND o.done_at IS NULL AND o.failed_at IS NULL`

	countReadyTasks = `
		SELECT kind, count(*)
		FROM outbox
		WHERE done_at IS NULL AND failed_at IS NULL AND next_at <= $1
		GROUP BY kind`
)

type taskResolution struct {
	FailureReason string     `json:"failure_reason"`
	ID            int64      `json:"id"`
	Attempts      int        `json:"attempts"`
	At            time.Time  `json:"at"`
	NextAt        *time.Time `json:"next_at"`
	Error         string     `json:"error"`
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
	return q.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		apply := func(tx *gorm.DB) error {
			count, err := execSQL(ctx, tx, resolveTasks, string(body))
			if err != nil {
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
}

// ReadyCounts не включает отложенные повторы и действующие аренды.
func (q *TaskQueue) ReadyCounts(ctx context.Context, now time.Time) (map[string]int64, error) {
	counts := map[string]int64{}
	err := q.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		rows, err := rawSQL(ctx, db, countReadyTasks, now).Rows()
		if err != nil {
			return err
		}
		defer func() {
			_ = rows.Close()
		}()
		for rows.Next() {
			var kind string
			var count int64
			if err := rows.Scan(&kind, &count); err != nil {
				return err
			}
			counts[kind] = count
		}
		return rows.Err()
	})
	return counts, err
}
