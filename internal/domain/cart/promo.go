package cart

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// MaxPromoLength — предел длины кода в символах: длиннее не бывает у магазинов, а запрос к серверу
// с мусорной строкой не нужен.
const MaxPromoLength = 200

// PromoCode — результат проверки кода сервером.
type PromoCode struct {
	Code     string      `json:"code"`
	Valid    bool        `json:"valid"`
	Discount money.Money `json:"discount"`
	Message  *string     `json:"message"`
}

// Discount — скидка на строку в копейках.
type Discount struct {
	PromotionID string      `json:"promotionId"`
	Amount      money.Money `json:"amount"`
}

// ClonePromos — отделяет снимок промокодов от агрегата.
func ClonePromos(values map[ids.TenantID]PromoCode) map[ids.TenantID]PromoCode {
	out := make(map[ids.TenantID]PromoCode, len(values))
	for tenant, promo := range values {
		if promo.Message != nil {
			message := *promo.Message
			promo.Message = &message
		}
		out[tenant] = promo
	}
	return out
}

// SetPromo — сохраняет код группы или удаляет его при пустом значении.
func (c *Cart) SetPromo(tenant ids.TenantID, code string, now time.Time, ttl time.Duration) (bool, error) {
	code = strings.TrimSpace(code)
	if utf8.RuneCountInString(code) > MaxPromoLength {
		return false, ErrPromoInvalid
	}
	found := false
	for _, line := range c.state.Lines {
		found = found || line.Offer.Tenant == tenant
	}
	if !found {
		return false, ErrLineNotFound
	}
	if current, ok := c.state.Promos[tenant]; (ok && current.Code == code) || (!ok && code == "") {
		return false, nil
	}
	if c.state.Promos == nil {
		c.state.Promos = make(map[ids.TenantID]PromoCode)
	}
	if code == "" {
		delete(c.state.Promos, tenant)
	} else {
		c.state.Promos[tenant] = PromoCode{Code: code}
	}
	for i := range c.state.Lines {
		if c.state.Lines[i].Offer.Tenant == tenant {
			c.state.Lines[i].Discounts = nil
		}
	}
	c.touch(now, ttl)
	return true, nil
}

// ApplyPromo — применяет проверенные сервером скидки группы.
func (c *Cart) ApplyPromo(
	tenant ids.TenantID,
	promo PromoCode,
	discounts map[ids.ProductID][]Discount,
	now time.Time,
	ttl time.Duration,
) bool {
	previous := c.state.Promos[tenant]
	changed := previous.Code != promo.Code || previous.Valid != promo.Valid || previous.Discount != promo.Discount
	if previous.Message == nil || promo.Message == nil {
		changed = changed || previous.Message != promo.Message
	} else {
		changed = changed || *previous.Message != *promo.Message
	}
	if c.state.Promos == nil {
		c.state.Promos = make(map[ids.TenantID]PromoCode)
	}
	if promo.Code != "" {
		c.state.Promos[tenant] = ClonePromos(map[ids.TenantID]PromoCode{tenant: promo})[tenant]
	} else {
		delete(c.state.Promos, tenant)
	}
	for i := range c.state.Lines {
		line := &c.state.Lines[i]
		if line.Offer.Tenant == tenant {
			changed = changed || !slices.Equal(line.Discounts, discounts[line.Offer.Product])
			line.Discounts = append([]Discount(nil), discounts[line.Offer.Product]...)
		}
	}
	if changed {
		c.touch(now, ttl)
	}
	return changed
}

// DiscountedAmount — вычисляет итог строки с проверкой переполнения.
func DiscountedAmount(price money.Money, quantity int, discounts []Discount) (money.Money, error) {
	amount, err := price.MulRatio(int64(quantity), 1)
	if err != nil {
		return money.Money{}, err
	}
	for _, discount := range discounts {
		if discount.Amount.IsNegative() {
			return money.Money{}, money.ErrInvalid
		}
		amount, err = amount.Sub(discount.Amount)
		if err != nil {
			return money.Money{}, err
		}
	}
	if amount.IsNegative() {
		return money.Money{}, money.ErrInvalid
	}
	return amount, nil
}

func (c *Cart) resetPromo(tenant ids.TenantID) {
	if promo, ok := c.state.Promos[tenant]; ok {
		c.state.Promos[tenant] = PromoCode{Code: promo.Code}
	}
	for i := range c.state.Lines {
		if c.state.Lines[i].Offer.Tenant == tenant {
			c.state.Lines[i].Discounts = nil
		}
	}
}
