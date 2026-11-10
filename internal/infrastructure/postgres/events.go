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
	insertProcessedEvent = `
		INSERT INTO processed_events (id, processed_at)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING`

	insertParkedEvent = `
		INSERT INTO parked_events (
			topic, kafka_partition, kafka_offset, message_key, headers, value, tenant_id, reason, error,
			attempts, parked_at, published_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (topic, kafka_partition, kafka_offset) DO NOTHING`
)

// Inbox — отметки обработанных событий.
type Inbox struct {
	db *gorm.DB
}

// Parked — отравленные сообщения.
type Parked struct {
	db *gorm.DB
}

// MarkProcessed отмечает событие; false — уже было.
func (i *Inbox) MarkProcessed(ctx context.Context, eventID string, at time.Time) (bool, error) {
	affected, err := execSQL(ctx, i.db, insertProcessedEvent, eventID, at)
	if err != nil {
		return false, fmt.Errorf("отметка события %s: %w", eventID, err)
	}

	return affected == 1, nil
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

	_, err = execSQL(
		ctx,
		p.db,
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
		event.PublishedAt,
	)
	if err != nil {
		return fmt.Errorf("парковка %s/%d/%d: %w", event.Topic, event.Partition, event.Offset, err)
	}

	return nil
}
