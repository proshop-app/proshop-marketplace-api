package checkout

import (
	"errors"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type paymentAttempt struct {
	token     string
	orders    map[ids.TenantID]string
	previous  map[ids.TenantID]bool
	attempted map[ids.TenantID]bool
	result    checkout.PaymentResult
}

// PaymentFailure сохраняет проверенный снимок для HTTP-отказа без второго чтения покупки.
type PaymentFailure struct {
	State checkout.State
	Cause error
}

func (e *PaymentFailure) Error() string { return e.Cause.Error() }
func (e *PaymentFailure) Unwrap() error { return e.Cause }

func paymentFailure(state checkout.State, err error) error {
	if err == nil {
		return nil
	}
	return &PaymentFailure{State: state, Cause: err}
}

func paymentHolds(state checkout.State) map[ids.TenantID]string {
	holds := make(map[ids.TenantID]string)
	for _, group := range state.Groups {
		if group.PaymentHold != "" {
			holds[group.TenantID] = group.PaymentHold
		}
	}
	return holds
}

func paymentRefusal(tenant ids.TenantID, err error, attempt *paymentAttempt) error {
	var refusal *ports.OrderRefusal
	if errors.As(err, &refusal) {
		switch refusal.Code {
		case "MARKETPLACE_STUB_PAYMENT_AMOUNT", "ORDER_NOT_FOUND":
			if !attempt.previous[tenant] {
				attempt.result.Unpaid[tenant] = true
			}
			return checkout.ErrTransition.Wrap(err)
		case "MARKETPLACE_STUB_PAYMENT_STATE":
			return checkout.ErrTransition.Wrap(err)
		}
	}
	if errors.Is(err, ports.ErrMarketplaceDisabled) {
		return checkout.ErrTransition.Wrap(err)
	}
	return checkout.ErrPaymentUnavailable.Wrap(err)
}
