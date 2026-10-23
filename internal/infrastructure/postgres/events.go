package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	insertProcessedEvent = `
		INSERT INTO processed_events (id, processed_at)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING`

	insertParkedEvent = `
		INSERT INTO parked_events (
			topic, kafka_partition, kafka_offset, message_key, headers, value, tenant_id, reason, error,
			attempts, parked_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (topic, kafka_partition, kafka_offset) DO NOTHING`
)

// Inbox — отметки обработанных событий.
type Inbox struct {
	q querier
}

// Parked — отравленные сообщения.
type Parked struct {
	q querier
}

// MarkProcessed отмечает событие; false — уже было.
func (i *Inbox) MarkProcessed(ctx context.Context, eventID string, at time.Time) (bool, error) {
	tag, err := i.q.Exec(ctx, insertProcessedEvent, eventID, at)
	if err != nil {
		return false, fmt.Errorf("отметка события %s: %w", eventID, err)
	}

	return tag.RowsAffected() == 1, nil
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

	_, err = p.q.Exec(
		ctx,
		insertParkedEvent,
		event.Topic,
		event.Partition,
		event.Offset,
		event.Key,
		string(headers),
		event.Value,
		tenant,
		event.Reason,
		event.Error,
		event.Attempts,
		event.ParkedAt,
	)
	if err != nil {
		return fmt.Errorf("парковка %s/%d/%d: %w", event.Topic, event.Partition, event.Offset, err)
	}

	return nil
}
