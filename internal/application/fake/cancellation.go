package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CancelOrder сериализует запрет создания с созданием по тому же ключу.
func (p *Platform) CancelOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CancelOrderRequest,
) (ports.CancelOrderResult, error) {
	if err := p.platformCall(ctx, "cancel_order"); err != nil {
		return ports.CancelOrderResult{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancelRequests[tenant] = append(p.cancelRequests[tenant], request)
	if err := p.cancelErrors[tenant]; err != nil {
		return ports.CancelOrderResult{}, err
	}
	key := string(tenant) + ":" + request.ExternalCheckoutID + ":" + string(request.GroupID)
	if result, ok := p.cancelledOrders[key]; ok {
		return result, nil
	}
	result := ports.CancelOrderResult{Absent: true}
	if order, ok := p.orders[key]; ok {
		if order.Status != checkout.GroupAwaitingPayment && order.Status != checkout.GroupCancelled {
			return ports.CancelOrderResult{}, &ports.OrderNotCancellable{OrderID: order.OrderID, Status: order.Status}
		}
		order.Status, order.Reason = checkout.GroupCancelled, request.Reason
		p.orders[key] = order
		result = ports.CancelOrderResult{OrderID: order.OrderID}
	}
	p.cancelledOrders[key] = result
	return result, nil
}

// FailCancel задаёт временную ошибку команды магазина.
func (p *Platform) FailCancel(tenant ids.TenantID, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancelErrors[tenant] = err
}

// CancellationRequests возвращает снимок отправленных команд.
func (p *Platform) CancellationRequests(tenant ids.TenantID) []ports.CancelOrderRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ports.CancelOrderRequest(nil), p.cancelRequests[tenant]...)
}
