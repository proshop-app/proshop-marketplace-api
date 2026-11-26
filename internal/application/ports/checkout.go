package ports

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CheckoutBuyerReader читает сохранённую покупку с проверкой владельца и диагностикой ошибок.
type CheckoutBuyerReader interface {
	LoadForBuyer(ctx context.Context, buyerID, checkoutID ids.UUID) (*checkout.Checkout, error)
}

// CheckoutObserver передаёт переходы и ошибки чтения в журнал и метрики.
type CheckoutObserver interface {
	CheckoutReadObserver
	RecordCheckoutEvent(tenant ids.TenantID, checkoutID ids.UUID, event string)
}

// CheckoutReadObserver считает ошибку покупки; log разрешает запись причины в журнал.
type CheckoutReadObserver interface {
	RecordCheckoutReadFailure(ctx context.Context, tenant ids.TenantID, checkoutID ids.UUID, cause error, log bool)
}
