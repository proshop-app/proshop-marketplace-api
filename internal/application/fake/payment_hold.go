package fake

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Длительность удержания задаёт сервер; fake повторяет его договор.
const paymentHoldWindow = 5 * time.Minute

// PaymentHold повторяет однократное удержание магазина, включая повтор после его истечения.
func (p *Platform) PaymentHold(ctx context.Context, tenant ids.TenantID, id string) (checkout.SellerOrder, error) {
	if err := p.platformCall(ctx, "payment_hold"); err != nil {
		return checkout.SellerOrder{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.failing[tenant]; err != nil {
		return checkout.SellerOrder{}, err
	}
	for key, order := range p.orders {
		if order.OrderID != id || p.orderTenants[id] != tenant {
			continue
		}
		if order.Status == checkout.GroupCancelled {
			return checkout.SellerOrder{}, &ports.OrderRefusal{Code: "ORDER_NOT_AWAITING_PAYMENT"}
		}
		if order.Status != checkout.GroupAwaitingPayment || !order.PaymentHeldUntil.IsZero() {
			return order, nil
		}
		now := p.orderNow()
		if !now.Before(order.ExpiresAt) {
			return checkout.SellerOrder{}, &ports.OrderRefusal{Code: "ORDER_PAYMENT_EXPIRED"}
		}
		order.PaymentHeldUntil = now.Add(paymentHoldWindow)
		if order.PaymentHeldUntil.Before(order.ExpiresAt) {
			order.PaymentHeldUntil = order.ExpiresAt
		}
		order.Revision++
		p.orders[key] = order
		return order, nil
	}
	return checkout.SellerOrder{}, &ports.OrderRefusal{Code: "ORDER_NOT_FOUND"}
}

func (p *Platform) orderNow() time.Time {
	if p.OrderClock != nil {
		return p.OrderClock.Now()
	}
	return p.OrderNow
}
