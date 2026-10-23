package checkout

import (
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// GroupStatus — состояние независимого заказа магазина.
type GroupStatus string

const (
	// GroupCreating — команда создания ещё не завершена.
	GroupCreating GroupStatus = "creating_order"
	// GroupAwaitingPayment — магазин ждёт оплаты.
	GroupAwaitingPayment GroupStatus = "awaiting_payment"
	// GroupPaid — магазин подтвердил оплату.
	GroupPaid GroupStatus = "paid"
	// GroupCancelled — магазин подтвердил отмену неоплаченного заказа.
	GroupCancelled GroupStatus = "cancelled"
	// GroupShipped — заказ отправлен.
	GroupShipped GroupStatus = "shipped"
	// GroupDelivered — заказ получен.
	GroupDelivered GroupStatus = "delivered"
	// GroupReturned — возврат оформлен магазином.
	GroupReturned GroupStatus = "returned"
	// GroupSellerUnreachable — магазин не отвечает при создании или сверке заказа.
	GroupSellerUnreachable GroupStatus = "seller_unreachable"
	// GroupPriceChanged — магазин отказал по цене, группа вернулась в корзину.
	GroupPriceChanged GroupStatus = "price_changed"
)

// GroupDeliveryUnavailable — окончательный отказ, нужен новый ПВЗ и новая покупка.
const GroupDeliveryUnavailable GroupStatus = "delivery_unavailable"

// Contact — гостевой покупатель для кассы и доставки магазина.
type Contact struct {
	Phone string `json:"phone"`
	Name  string `json:"name"`
}

// PriceChange — актуальная цена из отказа PRICE_CHANGED.
type PriceChange struct {
	ProductID ids.ProductID `json:"productId"`
	Price     money.Money   `json:"price"`
}

// SellerOrder — подтверждённый сервером снимок; возврат с кассы его не заменяет.
type SellerOrder struct {
	OrderID    string
	PaymentURL string
	ExpiresAt  time.Time
	Total      money.Money
	Status     GroupStatus
	Reason     string
	Track      string
}

// PaymentLink — следующий шаг оплаты с сохранённой позицией в очереди.
type PaymentLink struct {
	TenantID  ids.TenantID `json:"tenantId"`
	URL       string       `json:"url"`
	ExpiresAt time.Time    `json:"expiresAt"`
	Position  int          `json:"position"`
	Count     int          `json:"count"`
}

// BeginOrders сохраняет неизменный запрос до первого внешнего вызова.
func (c *Checkout) BeginOrders(contact Contact, returnURL string, now time.Time) (bool, error) {
	if c.state.Status != StatusConfirmed {
		if c.state.Status == StatusDraft {
			return false, ErrTransition
		}
		return false, nil
	}
	phone, err := buyer.ParsePhone(contact.Phone)
	if err != nil || strings.TrimSpace(contact.Name) == "" || utf8.RuneCountInString(contact.Name) > 200 ||
		strings.IndexFunc(contact.Name, unicode.IsControl) >= 0 {
		return false, ErrInvalidContact
	}
	contact.Phone, contact.Name = string(phone), strings.TrimSpace(contact.Name)
	if !ValidURL(returnURL) {
		return false, ErrInvalidContact
	}
	c.state.Contact = contact
	c.state.ReturnURL = returnURL
	c.state.Status = StatusCreatingOrders
	order := make([]int, len(c.state.Groups))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		left, _ := c.state.Groups[order[i]].Goods.Add(c.state.Groups[order[i]].DeliveryPrice)
		right, _ := c.state.Groups[order[j]].Goods.Add(c.state.Groups[order[j]].DeliveryPrice)
		return left.Cmp(right) > 0
	})
	for position, i := range order {
		c.state.Groups[i].PaymentOrder = position + 1
		c.state.Groups[i].Status = GroupCreating
	}
	return true, c.touch(now)
}

// AttachOrder допускает восстановление после потери ответа, включая уже истёкшее окно.
func (c *Checkout) AttachOrder(tenant ids.TenantID, order SellerOrder, now time.Time) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if g.OrderID != "" {
		if g.OrderID == order.OrderID {
			return false, nil
		}
		return false, ErrTransition
	}
	if g.Status != GroupCreating && g.Status != GroupSellerUnreachable {
		return false, ErrTransition
	}
	total, err := g.Goods.Add(g.DeliveryPrice)
	if err != nil {
		return false, err
	}
	if order.OrderID == "" || !ValidURL(order.PaymentURL) || order.ExpiresAt.IsZero() || total.Cmp(order.Total) != 0 {
		return false, ErrInvalidOrder
	}
	g.OrderID, g.PaymentURL, g.ExpiresAt = order.OrderID, order.PaymentURL, order.ExpiresAt
	g.Status, g.Reason, g.NextPollAt = GroupAwaitingPayment, "", now
	g.AwaitingSince = now
	c.rollup()
	return true, c.touch(now)
}

// Observe принимает только допустимые переходы. Истечение окна запрещает ссылку,
// но отмену подтверждает магазин: он мог получить оплату перед границей окна.
func (c *Checkout) Observe(tenant ids.TenantID, order SellerOrder, next time.Time, now time.Time) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	from := g.Status
	if from == GroupSellerUnreachable {
		from = g.LastOrderStatus
	}
	if g.OrderID == "" || g.OrderID != order.OrderID || !allowed(from, order.Status) {
		return false, ErrTransition
	}
	if order.Status == GroupCancelled && order.Reason == "" {
		return false, ErrInvalidOrder
	}
	if g.Status == order.Status && g.Reason == order.Reason && g.Track == order.Track && g.NextPollAt.Equal(next) {
		return false, nil
	}
	g.Status, g.Reason, g.Track = order.Status, order.Reason, order.Track
	g.NextPollAt = next
	g.PollFailures = 0
	g.LastErrorCode = ""
	g.LastOrderStatus = order.Status
	c.rollup()
	return true, c.touch(now)
}

// Payment возвращает ссылку только для ожидающей группы с действующим окном.
func (c *Checkout) Payment(tenant ids.TenantID, now time.Time) (*PaymentLink, error) {
	g := c.group(tenant)
	if g == nil {
		return nil, ErrNotFound
	}
	if g.Status != GroupAwaitingPayment {
		return nil, ErrTransition
	}
	if !now.Before(g.ExpiresAt) {
		return nil, ErrPaymentExpired
	}
	current := c.NextPayment(now)
	if current == nil || current.TenantID != tenant {
		return nil, ErrPaymentOrder
	}
	return current, nil
}

// NextPayment выбирает только текущий магазин с действующим окном.
func (c *Checkout) NextPayment(now time.Time) *PaymentLink {
	var current *Group
	for _, g := range c.state.Groups {
		if g.Status != GroupAwaitingPayment || !now.Before(g.ExpiresAt) {
			continue
		}
		if current == nil || g.PaymentOrder < current.PaymentOrder {
			copy := g
			current = &copy
		}
	}
	if current == nil {
		return nil
	}
	return &PaymentLink{
		TenantID:  current.TenantID,
		URL:       current.PaymentURL,
		ExpiresAt: current.ExpiresAt,
		Position:  current.PaymentOrder,
		Count:     len(c.state.Groups),
	}
}

// MarkCartConsumed отмечает атомарное изъятие снимка из корзины.
func (c *Checkout) MarkCartConsumed(now time.Time) (bool, error) {
	if c.state.CartConsumed {
		return false, nil
	}
	if c.state.Status == StatusDraft || c.state.Status == StatusConfirmed {
		return false, ErrTransition
	}
	c.state.CartConsumed = true
	return true, c.touch(now)
}

func (c *Checkout) group(tenant ids.TenantID) *Group {
	for i := range c.state.Groups {
		if c.state.Groups[i].TenantID == tenant {
			return &c.state.Groups[i]
		}
	}
	return nil
}

func (c *Checkout) rollup() {
	paid, cancelled, pending := 0, 0, 0
	for _, g := range c.state.Groups {
		status := g.Status
		if status == GroupSellerUnreachable && g.OrderID != "" && g.LastOrderStatus != "" {
			status = g.LastOrderStatus
		}
		switch status {
		case GroupPaid, GroupShipped, GroupDelivered, GroupReturned:
			paid++
		case GroupCancelled, GroupPriceChanged, GroupDeliveryUnavailable:
			cancelled++
		case GroupCreating, GroupSellerUnreachable:
			pending++
		}
	}
	switch {
	case paid == len(c.state.Groups):
		c.state.Status = StatusPaid
	case paid > 0:
		c.state.Status = StatusPartiallyPaid
	case cancelled == len(c.state.Groups):
		c.state.Status = StatusCancelled
	case pending > 0:
		c.state.Status = StatusCreatingOrders
	default:
		c.state.Status = StatusAwaitingPayment
	}
}

func allowed(from, to GroupStatus) bool {
	switch from {
	case GroupAwaitingPayment:
		return to == GroupAwaitingPayment || to == GroupCancelled || to == GroupPaid ||
			to == GroupShipped || to == GroupDelivered || to == GroupReturned
	case GroupPaid:
		return to == GroupPaid || to == GroupShipped || to == GroupDelivered || to == GroupReturned
	case GroupShipped:
		return to == GroupShipped || to == GroupDelivered || to == GroupReturned
	case GroupDelivered:
		return to == GroupDelivered || to == GroupReturned
	case GroupReturned:
		return to == GroupReturned
	case GroupCancelled:
		return to == GroupCancelled
	}
	return false
}

// ValidURL допускает абсолютные ссылки HTTP(S) без учётных данных.
func ValidURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}

func cloneState(state State) State {
	state.Groups = append([]Group(nil), state.Groups...)
	for i := range state.Groups {
		g := &state.Groups[i]
		g.Lines = append([]Line(nil), g.Lines...)
		g.CurrentPrices = append([]PriceChange(nil), g.CurrentPrices...)
		g.ReturnRemaining = append([]Line(nil), g.ReturnRemaining...)
		if g.Quote != nil {
			q := *g.Quote
			g.Quote = &q
		}
	}
	if state.Delivery != nil {
		d := *state.Delivery
		state.Delivery = &d
	}
	return state
}
