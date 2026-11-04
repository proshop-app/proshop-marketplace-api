package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	selectPendingReferenceTasks = `
		SELECT id, payload, attempts, created_at, COALESCE(last_error, '')
		FROM outbox
		WHERE kind = 'reference.changed' AND done_at IS NULL
			AND last_error IS DISTINCT FROM 'ownership-transferred'
		ORDER BY id`
	parkReferenceTasks = `
		UPDATE outbox
		SET next_at = 'infinity',
			last_error = 'ownership-transferred'
		WHERE kind = 'reference.changed' AND done_at IS NULL AND id = ANY($1::bigint[])`
)

// PendingReferenceTask — сохраняемый экспорт старого emitter. Payload и
// исходная строка outbox остаются: parked не означает опубликованное событие.
type PendingReferenceTask struct {
	ID        int64           `json:"id"`
	Payload   json.RawMessage `json:"payload"`
	Attempts  int             `json:"attempts"`
	CreatedAt time.Time       `json:"createdAt"`
	LastError string          `json:"lastError"`
}

// PendingReferenceTasks читает только задачи прежнего владельца для явного экспорта.
func PendingReferenceTasks(ctx context.Context, pool *pgxpool.Pool) ([]PendingReferenceTask, error) {
	rows, err := pool.Query(ctx, selectPendingReferenceTasks)
	if err != nil {
		return nil, fmt.Errorf("экспорт задач реестра: %w", err)
	}
	defer rows.Close()
	result := []PendingReferenceTask{}
	for rows.Next() {
		var task PendingReferenceTask
		if err := rows.Scan(&task.ID, &task.Payload, &task.Attempts, &task.CreatedAt, &task.LastError); err != nil {
			return nil, err
		}
		result = append(result, task)
	}
	return result, rows.Err()
}

// ParkReferenceTasks вызывается только после записи и Sync файла экспорта.
// done_at не трогается: чужая доставка не выдаётся за успешную публикацию.
func ParkReferenceTasks(ctx context.Context, pool *pgxpool.Pool, ids []int64) error {
	result, err := pool.Exec(ctx, parkReferenceTasks, ids)
	if err != nil {
		return err
	}
	if result.RowsAffected() != int64(len(ids)) {
		return fmt.Errorf("задачи реестра изменились после экспорта: экспорт повторить новым файлом")
	}
	return nil
}
