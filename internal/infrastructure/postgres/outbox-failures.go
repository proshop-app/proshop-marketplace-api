package postgres

import (
	"context"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const selectFailedTasks = `SELECT id, kind, tenant_id, attempts, failure_reason, failed_at, created_at
    FROM outbox WHERE failed_at IS NOT NULL AND id > $1 ORDER BY id LIMIT $2`

// Failures читает только безопасные поля; исходные payload и last_error доступны в служебном хранилище.
func (q *TaskQueue) Failures(ctx context.Context, after int64, limit int) ([]ports.TaskFailure, error) {
	items := []ports.TaskFailure{}
	err := q.source.withConnection(ctx, func(db *gorm.DB) error {
		rows, err := rawSQL(ctx, db, selectFailedTasks, after, limit).Rows()
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var item ports.TaskFailure
			if err := rows.Scan(
				&item.ID,
				&item.Kind,
				&item.TenantID,
				&item.Attempts,
				&item.Reason,
				&item.FailedAt,
				&item.CreatedAt,
			); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}
