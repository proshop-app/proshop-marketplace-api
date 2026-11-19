package moderation

import "github.com/proshop-app/proshop-marketplace/pkg/ids"

// BulkCase требует версию, которую оператор видел в очереди.
type BulkCase struct {
	ID              ids.UUID `json:"id"`
	ExpectedVersion int64    `json:"expectedVersion"`
}

// BulkSelector читает только очередную страницу подходящих открытых дел.
type BulkSelector struct {
	TenantID ids.TenantID `json:"tenantId"`
	Reasons  []string     `json:"reasons"`
	After    ids.UUID     `json:"after,omitempty"`
}

// BulkSkip объясняет отказ независимо от дальнейших изменений дела.
type BulkSkip struct {
	ID     ids.UUID `json:"id"`
	Reason string   `json:"reason"`
}

// BulkResult сохраняется атомарно с решениями страницы.
type BulkResult struct {
	Approved  int        `json:"approved"`
	Skipped   int        `json:"skipped"`
	Details   []BulkSkip `json:"details"`
	NextAfter ids.UUID   `json:"nextAfter,omitempty"`
	HasMore   bool       `json:"hasMore"`
}
