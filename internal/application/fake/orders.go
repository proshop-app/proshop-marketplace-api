package fake

import (
	"context"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// CreateOrder сохраняет уникальный заказ до возврата ответа приложению.
func (p *Platform) CreateOrder(
	_ context.Context,
	tenant ids.TenantID,
	request ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.failing[tenant]; err != nil {
		return checkout.SellerOrder{}, err
	}
	key := string(tenant) + ":" + request.ExternalCheckoutID + ":" + string(request.GroupID)
	if existing, ok := p.orders[key]; ok {
		return existing, nil
	}
	if refused, ok := p.orderRefusals[key]; ok {
		return checkout.SellerOrder{}, refused
	}
	total := request.DeliveryPrice
	for _, item := range request.Items {
		amount, err := item.Price.MulRatio(int64(item.Quantity), 1)
		if err != nil {
			return checkout.SellerOrder{}, err
		}
		total, err = total.Add(amount)
		if err != nil {
			return checkout.SellerOrder{}, err
		}
		if current, ok := p.items[tenant][item.ProductID]; ok && current.Price != item.Price {
			refused := &ports.PriceChanged{Items: []checkout.PriceChange{
				{ProductID: item.ProductID, Price: current.Price},
			}}
			p.orderRefusals[key] = refused
			return checkout.SellerOrder{}, refused
		}
	}
	order := checkout.SellerOrder{
		OrderID:    fmt.Sprintf("order-%d", len(p.orders)+1),
		PaymentURL: "https://seller.example/pay/" + request.ExternalCheckoutID,
		ExpiresAt:  p.OrderNow.Add(time.Hour),
		Total:      total,
		Status:     checkout.GroupAwaitingPayment,
	}
	p.orders[key] = order
	p.orderTenants[order.OrderID] = tenant
	if p.AfterCreate != nil {
		p.AfterCreate()
	}
	return order, nil
}

// GetOrder отдаёт заданный тестом статус заказа этого магазина.
func (p *Platform) GetOrder(_ context.Context, tenant ids.TenantID, id string) (checkout.SellerOrder, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.failing[tenant]; err != nil {
		return checkout.SellerOrder{}, err
	}
	for _, order := range p.orders {
		if order.OrderID == id && p.orderTenants[id] == tenant {
			return order, nil
		}
	}
	return checkout.SellerOrder{}, checkout.ErrNotFound
}

// SetOrderStatus моделирует оплату и исполнение на стороне магазина.
func (p *Platform) SetOrderStatus(id string, status checkout.GroupStatus, reason, track string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, order := range p.orders {
		if order.OrderID != id {
			continue
		}
		order.Status, order.Reason, order.Track = status, reason, track
		p.orders[key] = order
	}
}

// OrderCount считает созданные заказы для проверки идемпотентности.
func (p *Platform) OrderCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.orders)
}

// SetOrderTotal позволяет проверить отказ при неверной сумме ответа сервера.
func (p *Platform) SetOrderTotal(id string, total money.Money) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, order := range p.orders {
		if order.OrderID == id {
			order.Total = total
			p.orders[key] = order
		}
	}
}
