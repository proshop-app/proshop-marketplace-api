package postgres

import (
	"context"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const requeueFailedTasks = `WITH selected AS (
    SELECT id FROM outbox
    WHERE failed_at IS NOT NULL AND id > $1
        AND ($2::bigint = 0 OR id = $2) AND ($3::text = '' OR kind = $3)
    ORDER BY id LIMIT $4 FOR UPDATE SKIP LOCKED
), updated AS (
    UPDATE outbox o SET
        attempts = o.attempts + 1,
        failed_at = NULL,
        failure_reason = NULL,
        unknown_failures = NULL,
        last_error = NULL,
        next_at = $5
    FROM selected s WHERE o.id = s.id
    RETURNING o.id, o.kind
)
SELECT id, kind FROM updated ORDER BY id`

// Requeue увеличивает attempts: старый обработчик не получит право на ожившую задачу.
func (q *TaskQueue) Requeue(ctx context.Context, request ports.TaskRequeue) ([]int64, error) {
	items := []int64{}
	hints := make(ports.WorkHints)
	err := q.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		rows, err := rawSQL(
			ctx,
			db,
			requeueFailedTasks,
			request.After,
			request.ID,
			request.Kind,
			request.Limit,
			request.At,
		).Rows()
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id int64
			var kind string
			if err := rows.Scan(&id, &kind); err != nil {
				return err
			}
			items = append(items, id)
			if q.wake != nil {
				hints[kind] = request.At
			}
		}
		return rows.Err()
	})
	if err == nil && len(hints) > 0 {
		q.wake.Notify(hints)
	}
	return items, err
}
