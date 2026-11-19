package moderation

import "github.com/proshop-app/proshop-marketplace/pkg/ids"

// BulkCase требует версию, которую оператор видел в очереди.
type BulkCase struct {
	ID              ids.UUID `json:"id"`
	ExpectedVersion int64    `json:"expectedVersion"`
}

// BulkSelector читает только очередную страницу подходящих открытых дел.
type BulkSelector struct {
	TenantID ids.TenantID `json:"tenantId,omitempty"`
	Reasons  []string     `json:"reasons,omitempty"`
	Source   string       `json:"source,omitempty"`
	Reason   string       `json:"reason,omitempty"`
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

// BulkCounts раскрывает объём и риск отбора до подтверждения оператором.
type BulkCounts struct {
	Total        int `json:"total"`
	Tenants      int `json:"tenants"`
	OtherReasons int `json:"otherReasons"`
}
