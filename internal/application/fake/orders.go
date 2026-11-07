package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
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
	key := string(tenant) + ":" + request.ExternalCheckoutID + ":" + string(request.GroupID)
	request.Items = append([]ports.OrderItem(nil), request.Items...)
	p.orderRequests[tenant] = append(p.orderRequests[tenant], request)
	raw, _ := json.Marshal(request)
	if body, ok := p.orderBodies[key]; ok && body != string(raw) {
		return checkout.SellerOrder{}, &ports.OrderRefusal{Code: "IDEMPOTENCY_CONFLICT"}
	}
	p.orderBodies[key] = string(raw)
	if err := p.failing[tenant]; err != nil {
		return checkout.SellerOrder{}, err
	}
	if existing, ok := p.orders[key]; ok {
		return existing, nil
	}
	if refused, ok := p.orderRefusals[key]; ok {
		return checkout.SellerOrder{}, refused
	}
	if err := p.createErrors[tenant]; err != nil {
		var refusal *ports.OrderRefusal
		if errors.As(err, &refusal) && !refusal.Unexpected && (refusal.Code == "DELIVERY_UNAVAILABLE" ||
			refusal.Code == "DELIVERY_CHANGED" || refusal.Code == "OUT_OF_STOCK") {
			p.orderRefusals[key] = err
		}
		return checkout.SellerOrder{}, err
	}
	if request.PromoCode != "" {
		promo, ok := p.promos[tenant]
		if !ok || promo.PromoCode == nil || !promo.PromoCode.Valid {
			refusal := checkout.ErrPromoInvalid
			if promo.PromoCode != nil && promo.PromoCode.Message != nil {
				refusal = refusal.WithMessage(*promo.PromoCode.Message)
			}
			p.orderRefusals[key] = refusal
			return checkout.SellerOrder{}, refusal
		}
	}
	total := request.DeliveryPrice
	for _, item := range request.Items {
		discounts := item.Discounts
		current := p.promos[tenant]
		if request.PromoCode == "" && current.PromoCode != nil {
			current = ports.GroupPricing{}
		}
		actual := current.Discounts[item.ProductID]
		if !sameDiscounts(discounts, actual) {
			refused := &ports.PriceChanged{Items: []checkout.PriceChange{
				{ProductID: item.ProductID, Price: item.Price},
			}}
			p.orderRefusals[key] = refused
			return checkout.SellerOrder{}, refused
		}
		amount, err := cart.DiscountedAmount(item.Price, item.Quantity, actual)
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
	if err := p.validateOrderDelivery(tenant, request); err != nil {
		p.orderRefusals[key] = err
		return checkout.SellerOrder{}, err
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
	p.pollCalls[tenant]++
	if err := p.pollErrors[tenant]; err != nil {
		return checkout.SellerOrder{}, err
	}
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
func (p *Platform) SetOrderStatus(id string, status checkout.GroupStatus, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, order := range p.orders {
		if order.OrderID != id {
			continue
		}
		order.Status, order.Reason = status, reason
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

// FailCreate моделирует отказ создания независимо от проверки настроек и расчёта.
func (p *Platform) FailCreate(tenant ids.TenantID, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.createErrors[tenant] = err
}

// FailPoll моделирует отказ опроса уже созданного заказа.
func (p *Platform) FailPoll(tenant ids.TenantID, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pollErrors[tenant] = err
}

// PollCalls — обращения к магазину, включая неудачные.
func (p *Platform) PollCalls(tenant ids.TenantID) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pollCalls[tenant]
}

// OrderRequests возвращает копии тел всех попыток создания для проверки неизменности.
func (p *Platform) OrderRequests(tenant ids.TenantID) []ports.CreateOrderRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := append([]ports.CreateOrderRequest(nil), p.orderRequests[tenant]...)
	for i := range result {
		result[i].Items = append([]ports.OrderItem(nil), result[i].Items...)
	}
	return result
}

func (p *Platform) validateOrderDelivery(tenant ids.TenantID, request ports.CreateOrderRequest) error {
	settings, hasSettings := p.settings[tenant]
	offered, hasQuote := p.quotes[tenant]
	if !hasSettings || !hasQuote {
		return nil
	}
	if !offered.Available {
		return &ports.OrderRefusal{Code: "DELIVERY_UNAVAILABLE"}
	}
	goods := money.Money{}
	for _, item := range request.Items {
		current := p.promos[tenant]
		if request.PromoCode == "" && current.PromoCode != nil {
			current = ports.GroupPricing{}
		}
		discounts := current.Discounts[item.ProductID]
		amount, err := cart.DiscountedAmount(item.Price, item.Quantity, discounts)
		if err != nil {
			return err
		}
		goods, err = goods.Add(amount)
		if err != nil {
			return err
		}
	}
	contribution, err := (checkout.Group{Goods: goods}).DeliverySubsidy(
		settings.FreeDeliveryFrom,
		settings.Subsidy,
		offered.Cost,
		p.MinSubsidyPercent,
	)
	if err != nil {
		return err
	}
	fee, err := offered.Cost.Sub(contribution)
	if err != nil {
		return err
	}
	if fee != request.DeliveryPrice {
		return &ports.OrderRefusal{Code: "DELIVERY_CHANGED"}
	}
	return nil
}

// SetTracking — задаёт отслеживание заказа для сценария.
func (p *Platform) SetTracking(id string, tracking *checkout.Tracking) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, order := range p.orders {
		if order.OrderID == id {
			order.Tracking = tracking
			p.orders[key] = order
		}
	}
}

// Сервер сравнивает набор скидок независимо от их порядка в запросе.
func sameDiscounts(expected, actual []cart.Discount) bool {
	if len(expected) != len(actual) {
		return false
	}
	for _, discount := range expected {
		if !slices.Contains(actual, discount) {
			return false
		}
	}
	return true
}
