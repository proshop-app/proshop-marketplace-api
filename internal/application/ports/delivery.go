package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// PickupPoints — ответ магазина без транспортной обёртки.
type PickupPoints struct {
	NextCursor *string                `json:"nextCursor,omitempty"`
	Total      *int                   `json:"total,omitempty"`
	Available  bool                   `json:"available"`
	Reason     string                 `json:"reason,omitempty"`
	Items      []delivery.PickupPoint `json:"items"`
}

// PickupCache хранит неперсональные списки по службе и нормализованному городу.
type PickupCache interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte, time.Duration) error
}

// PickupLookup проверяет выбор; обязательный buyerID задаёт единый личный лимит холодных загрузок.
type PickupLookup interface {
	Point(context.Context, ids.UUID, delivery.Carrier, string, string) (delivery.PickupPoint, error)
}

// ProfilePickupLookup проверяет пункт профиля без резерва оформления покупки.
type ProfilePickupLookup interface {
	ProfilePoint(context.Context, delivery.Carrier, string, string) (delivery.PickupPoint, error)
}
