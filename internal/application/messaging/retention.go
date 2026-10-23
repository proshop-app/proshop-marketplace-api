package messaging

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// RetentionPolicy — сроки хранения служебных записей.
type RetentionPolicy struct {
	ProcessedEvents time.Duration
	OutboxDone      time.Duration
	ParkedEvents    time.Duration
	Batch           int
}

// Janitor чистит служебные записи по сроку: отметки обработанных событий,
// выполненные задачи outbox и разобранные отравленные сообщения.
type Janitor struct {
	retention ports.Retention
	clock     ports.Clock
	policy    RetentionPolicy
}

// NewJanitor — чистка по сроку.
func NewJanitor(retention ports.Retention, clock ports.Clock, policy RetentionPolicy) *Janitor {
	return &Janitor{retention: retention, clock: clock, policy: policy}
}

// Purge удаляет записи старше сроков политики.
func (j *Janitor) Purge(ctx context.Context) (ports.PurgeResult, error) {
	now := j.clock.Now()

	return j.retention.Purge(ctx, ports.PurgeRequest{
		ProcessedBefore:  now.Add(-j.policy.ProcessedEvents),
		OutboxDoneBefore: now.Add(-j.policy.OutboxDone),
		ParkedBefore:     now.Add(-j.policy.ParkedEvents),
		Batch:            j.policy.Batch,
	})
}
