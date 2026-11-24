package postgres

import (
	"context"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// selectOutboxRetryStats читает только покрывающий индекс повторов, включая их аренды.
const selectOutboxRetryStats = `SELECT kind, count(*), min(created_at)
    FROM outbox
    WHERE failed_at IS NULL
        AND (last_error IS NOT NULL OR unknown_failures IS NOT NULL)
    GROUP BY kind`

// RetryStats возвращает один срез по видам, независимо от срока следующей попытки.
func (q *TaskQueue) RetryStats(ctx context.Context) ([]ports.TaskRetryStats, error) {
	stats := []ports.TaskRetryStats{}
	err := q.source.withConnection(
		ctx,
		func(ctx context.Context, db *gorm.DB) error {
			rows, err := rawSQL(ctx, db, selectOutboxRetryStats).Rows()
			if err != nil {
				return err
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var item ports.TaskRetryStats
				if err := rows.Scan(&item.Kind, &item.Count, &item.Oldest); err != nil {
					return err
				}
				stats = append(stats, item)
			}
			return rows.Err()
		},
	)
	return stats, err
}
