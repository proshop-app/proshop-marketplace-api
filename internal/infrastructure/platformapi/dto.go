package platformapi

import (
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type promoCodeJSON struct {
	Code     string  `json:"code"`
	Valid    bool    `json:"valid"`
	Discount int64   `json:"discount"`
	Message  *string `json:"message"`
}

type discountJSON struct {
	PromotionID string `json:"promotionId"`
	Amount      int64  `json:"amount"`
}

type trackingJSON struct {
	Carrier string  `json:"carrier"`
	Number  *string `json:"number"`
	URL     string  `json:"url"`
}

func (p *promoCodeJSON) domain() *cart.PromoCode {
	if p == nil {
		return nil
	}
	return &cart.PromoCode{Code: p.Code, Valid: p.Valid, Discount: money.FromKopecks(p.Discount), Message: p.Message}
}

func domainDiscounts(values []discountJSON) []cart.Discount {
	result := make([]cart.Discount, 0, len(values))
	for _, v := range values {
		result = append(result, cart.Discount{PromotionID: v.PromotionID, Amount: money.FromKopecks(v.Amount)})
	}
	return result
}

func (t *trackingJSON) domain() *checkout.Tracking {
	if t == nil {
		return nil
	}
	return &checkout.Tracking{Carrier: t.Carrier, Number: t.Number, URL: t.URL}
}

// orderBody отделяет формат запроса сервера от снимка команды приложения.
func orderBody(r ports.CreateOrderRequest) map[string]any {
	items := make([]map[string]any, 0, len(r.Items))
	for _, item := range r.Items {
		line := map[string]any{"productId": item.ProductID, "quantity": item.Quantity, "price": item.Price.Kopecks()}
		discounts := make([]discountJSON, 0, len(item.Discounts))
		for _, d := range item.Discounts {
			discounts = append(discounts, discountJSON{PromotionID: d.PromotionID, Amount: d.Amount.Kopecks()})
		}
		line["discounts"] = discounts
		items = append(items, line)
	}
	body := map[string]any{
		"externalCheckoutId": r.ExternalCheckoutID,
		"groupId":            r.GroupID,
		"items":              items,
		"carrier":            r.Carrier,
		"pickupPointCode":    r.PickupPointCode,
		"contact":            map[string]string{"name": r.Contact.Name, "phone": r.Contact.Phone},
		"deliveryPrice":      r.DeliveryPrice.Kopecks(),
		"returnUrl":          r.ReturnURL,
	}
	if r.BuyerID != "" {
		body["buyerId"] = r.BuyerID
	}
	if r.PromoCode != "" {
		body["promoCode"] = r.PromoCode
	}
	return body
}
