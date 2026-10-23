package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// NewTask — задача для outbox. Пишется в той же транзакции, что и изменение,
// которое её порождает.
type NewTask struct {
	Kind     string
	TenantID ids.TenantID
	// DedupKey склеивает ещё не взятые задачи с одним ключом: пачка событий по
	// одному товару даёт одну синхронизацию.
	DedupKey string
	Payload  []byte
	// At — время постановки; задача созревает сразу.
	At time.Time
}

// Task — задача, взятая из outbox на разбор.
type Task struct {
	ID        int64
	Kind      string
	TenantID  ids.TenantID
	Payload   []byte
	Attempts  int
	CreatedAt time.Time
}

// Outbox — запись задач внутри транзакции.
type Outbox interface {
	Enqueue(ctx context.Context, task NewTask) error
}

// ClaimRequest — сколько задач взять и до какого времени их держать.
type ClaimRequest struct {
	Now        time.Time
	Limit      int
	PerTenant  int
	LeaseUntil time.Time
}

// TaskQueue — разбор outbox. Взятая задача держится арендой: упавший
// обработчик не теряет её, после аренды её возьмёт другой.
type TaskQueue interface {
	// Claim берёт созревшие задачи, не больше PerTenant на магазин: шумный
	// магазин не задерживает чужие задачи.
	Claim(ctx context.Context, request ClaimRequest) ([]Task, error)
	// Complete отмечает задачу выполненной.
	Complete(ctx context.Context, id int64, at time.Time) error
	// Retry откладывает задачу до nextAt с причиной.
	Retry(ctx context.Context, id int64, nextAt time.Time, reason string) error
}

// Inbox — учёт обработанных событий для дедупа.
type Inbox interface {
	// MarkProcessed отмечает событие; false — событие уже обработано.
	MarkProcessed(ctx context.Context, eventID string, at time.Time) (bool, error)
}

// ParkedEvent — сообщение, которое нельзя обработать: без магазина,
// повреждённое или исчерпавшее попытки.
type ParkedEvent struct {
	Topic     string
	Partition int
	Offset    int64
	Key       []byte
	Headers   map[string]string
	Value     []byte
	TenantID  string
	Reason    string
	Error     string
	Attempts  int
	ParkedAt  time.Time
}

// ParkedEvents — отравленные сообщения.
type ParkedEvents interface {
	// Park сохраняет сообщение; повтор той же позиции топика ничего не меняет.
	Park(ctx context.Context, event ParkedEvent) error
}

// PurgeRequest — границы чистки служебных записей.
type PurgeRequest struct {
	ProcessedBefore  time.Time
	OutboxDoneBefore time.Time
	ParkedBefore     time.Time
	Batch            int
}

// PurgeResult — сколько записей удалено.
type PurgeResult struct {
	Processed  int64
	OutboxDone int64
	Parked     int64
}

// Retention — чистка служебных записей по сроку.
type Retention interface {
	Purge(ctx context.Context, request PurgeRequest) (PurgeResult, error)
}
