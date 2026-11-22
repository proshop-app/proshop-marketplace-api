package fake

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CancelPaidOrder моделирует отмену до передачи перевозчику, повтор отвечает тем же.
func (p *Platform) CancelPaidOrder(
	ctx context.Context,
	tenant ids.TenantID,
	id string,
) (checkout.SellerOrder, error) {
	cancelOrder := func(order *checkout.SellerOrder) error {
		switch {
		case order.Status == checkout.GroupPaid || order.Status == checkout.GroupAssembled:
			order.Status, order.Reason = checkout.GroupReturned, checkout.CancelledBeforeShipment
			order.Revision++
		case order.Status == checkout.GroupReturned && order.Reason == checkout.CancelledBeforeShipment:
		default:
			return checkout.ErrOrderNotCancellable
		}
		return nil
	}
	return p.actOnOrder(ctx, tenant, id, "cancel_paid_order", cancelOrder)
}

// RequestReturn моделирует единственную заявку по полученному заказу.
func (p *Platform) RequestReturn(
	ctx context.Context,
	tenant ids.TenantID,
	id string,
	reason checkout.ReturnReason,
) (checkout.SellerOrder, error) {
	requestReturn := func(order *checkout.SellerOrder) error {
		if order.Status != checkout.GroupDelivered {
			return checkout.ErrReturnUnavailable
		}
		if order.ReturnRequest == nil {
			order.ReturnRequest = &checkout.ReturnRequest{
				Status:      checkout.ReturnRequested,
				Reason:      reason,
				RequestedAt: time.Date(2026, time.November, 28, 7, 14, 0, 0, time.UTC),
			}
			order.Revision++
		}
		return nil
	}
	return p.actOnOrder(ctx, tenant, id, "request_return", requestReturn)
}

// ActionCalls — сколько команд покупателя дошло до магазина.
func (p *Platform) ActionCalls(tenant ids.TenantID) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.actionCalls[tenant]
}

func (p *Platform) actOnOrder(
	ctx context.Context,
	tenant ids.TenantID,
	id, operation string,
	act func(*checkout.SellerOrder) error,
) (checkout.SellerOrder, error) {
	if err := p.platformCall(ctx, operation); err != nil {
		return checkout.SellerOrder{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.actionCalls[tenant]++
	if err := p.failing[tenant]; err != nil {
		return checkout.SellerOrder{}, err
	}
	for key, order := range p.orders {
		if order.OrderID != id || p.orderTenants[id] != tenant {
			continue
		}
		if err := act(&order); err != nil {
			return checkout.SellerOrder{}, err
		}
		p.orders[key] = order
		return order, nil
	}
	return checkout.SellerOrder{}, checkout.ErrNotFound
}
