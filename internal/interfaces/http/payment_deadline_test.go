package http

import (
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

func TestCheckoutResponseUsesEffectivePaymentDeadline(t *testing.T) {
	due := time.Date(2026, 11, 22, 10, 10, 0, 0, time.UTC)
	for _, held := range []time.Time{{}, due.Add(-time.Minute), due.Add(4 * time.Minute)} {
		group := checkout.Group{
			ExpiresAt:        due,
			PaymentHeldUntil: held,
		}
		response, err := checkoutGroupResponse(group)
		want := due
		if !held.IsZero() {
			want = held
		}
		if err != nil || response.ExpiresAt == nil || !response.ExpiresAt.Equal(want) {
			t.Fatal("публичный срок должен учитывать удержание", response, err)
		}
	}
}
