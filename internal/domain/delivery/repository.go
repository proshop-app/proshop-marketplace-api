package delivery

import (
	"context"
	"time"
)

// PickupPointRepository — справочник пунктов выдачи.
type PickupPointRepository interface {
	// Upsert записывает пункты одного обновления справочника.
	Upsert(ctx context.Context, points []PickupPoint) error
	// DeactivateStale закрывает пункты перевозчика, не пришедшие в обновлении, начатом в since.
	DeactivateStale(ctx context.Context, carrier Carrier, since time.Time) (int64, error)
	// Get — пункт; нет — ErrPickupPointNotFound.
	Get(ctx context.Context, carrier Carrier, code string) (PickupPoint, error)
	// Page — пункты перевозчика после кода after, включая закрытые: для переноса в индекс.
	Page(ctx context.Context, carrier Carrier, after string, limit int) ([]PickupPoint, error)
}
