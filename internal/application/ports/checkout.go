package ports

import "github.com/proshop-app/proshop-marketplace/pkg/ids"

// CheckoutObserver получает только зафиксированные переходы для журналов и метрик.
type CheckoutObserver interface {
	RecordCheckoutEvent(tenant ids.TenantID, checkoutID ids.UUID, event string)
}
