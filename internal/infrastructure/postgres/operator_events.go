package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/event"
)

const (
	eventColumns = `id, topic, kafka_partition, kafka_offset, message_key, headers, value,
  COALESCE(tenant_id, ''), reason, error, attempts, parked_at, published_at,
  status, version, resolution, operator_id, updated_at`
	eventPredicates = ` WHERE ($1 = '' OR tenant_id = $1) AND ($2 = '' OR status = $2)
  AND ($3 = '' OR topic = $3) AND ($4 = '' OR reason = $4)`
	selectOperatorEvents = `SELECT ` + eventColumns + ` FROM parked_events` + eventPredicates +
		` ORDER BY id DESC LIMIT $5 OFFSET $6`
	countOperatorEvents = `SELECT count(*) FROM parked_events` + eventPredicates
	selectOperatorEvent = `SELECT ` + eventColumns + ` FROM parked_events WHERE id = $1`
	insertEventDecision = `INSERT INTO parked_event_decisions (event_id, version, status, note, operator_id, decided_at)
 VALUES ($1, $2, $3, $4, $5, $6)`
	updateOperatorEvent = `UPDATE parked_events
  SET status = $1,
   version = $2,
   resolution = $3,
   operator_id = $4,
   updated_at = $5,
   error = CASE WHEN status = 'queued' THEN $6 ELSE error END,
   attempts = attempts + CASE WHEN status = 'queued' THEN $10 ELSE 0 END
  WHERE id = $7 AND status = $8 AND version = $9`
)

// Get читает исходное сообщение вместе с текущим решением.
func (p *Parked) Get(ctx context.Context, id int64) (ports.ParkedEvent, error) {
	return scanOperatorEvent(rawSQL(ctx, p.db, selectOperatorEvent, id).Row())
}

// List — страницы в порядке убывания неизменяемого id.
func (p *Parked) List(ctx context.Context, f ports.EventFilter) ([]ports.ParkedEvent, int, error) {
	var total int
	row := rawSQL(ctx, p.db, countOperatorEvents, f.TenantID, f.Status, f.Topic, f.Reason).Row()
	if err := row.Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("число событий: %w", err)
	}
	rows, err := rawSQL(
		ctx,
		p.db,
		selectOperatorEvents,
		f.TenantID,
		f.Status,
		f.Topic,
		f.Reason,
		f.Limit,
		f.Offset,
	).Rows()
	if err != nil {
		return nil, 0, fmt.Errorf("список событий: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	items := []ports.ParkedEvent{}
	for rows.Next() {
		item, err := scanOperatorEvent(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

// Update сохраняет переход и аудит при совпадении исходных статуса и версии.
func (p *Parked) Update(ctx context.Context, aggregate *event.Event, failure string, attempts int) error {
	state, origin := aggregate.State(), aggregate.Origin()
	affected, err := execSQL(
		ctx,
		p.db,
		updateOperatorEvent,
		state.Status,
		state.Version,
		state.Resolution,
		state.OperatorID,
		state.UpdatedAt,
		failure,
		state.ID,
		origin.Status,
		origin.Version,
		attempts,
	)
	if err != nil {
		return fmt.Errorf("решение по событию: %w", err)
	}
	if affected != 1 {
		return event.ErrConflict
	}
	_, err = execSQL(
		ctx,
		p.db,
		insertEventDecision,
		state.ID,
		state.Version,
		state.Status,
		state.Resolution,
		state.OperatorID,
		state.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("аудит события: %w", err)
	}
	aggregate.MarkSaved()
	return nil
}

func scanOperatorEvent(row rowScanner) (ports.ParkedEvent, error) {
	var value ports.ParkedEvent
	var headers []byte
	err := row.Scan(
		&value.Control.ID,
		&value.Topic,
		&value.Partition,
		&value.Offset,
		&value.Key,
		&headers,
		&value.Value,
		&value.TenantID,
		&value.Reason,
		&value.Error,
		&value.Attempts,
		&value.ParkedAt,
		&value.PublishedAt,
		&value.Control.Status,
		&value.Control.Version,
		&value.Control.Resolution,
		&value.Control.OperatorID,
		&value.Control.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return value, event.ErrNotFound
	}
	if err != nil {
		return value, fmt.Errorf("чтение события: %w", err)
	}
	if err := json.Unmarshal(headers, &value.Headers); err != nil {
		return value, err
	}
	return value, nil
}
