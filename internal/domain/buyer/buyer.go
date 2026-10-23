package buyer

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Buyer — покупатель маркетплейса. Один телефон — один покупатель.
type Buyer struct {
	ID        ids.UUID
	Phone     Phone
	CreatedAt time.Time
}

// New — новый покупатель.
func New(id ids.UUID, phone Phone, now time.Time) Buyer {
	return Buyer{ID: id, Phone: phone, CreatedAt: now}
}
