package checkout

import (
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// GroupStatus — состояние независимого заказа магазина.
type GroupStatus string

// Статусы независимой группы: локальное решение не подменяет подтверждение магазина.
const (
	GroupCancellationRequested GroupStatus = "cancellation_requested"
	GroupRejected              GroupStatus = "rejected"
	// GroupCreating — команда создания ещё не завершена.
	GroupCreating GroupStatus = "creating_order"
	// GroupUnconfirmed — срок подтверждения магазина истёк.
	GroupUnconfirmed GroupStatus = "unconfirmed"
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

// Contact — гостевой покупатель для заказа и доставки магазина.
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
	Tracking  *Tracking
	OrderID   string
	ExpiresAt time.Time
	Total     money.Money
	Status    GroupStatus
	Reason    string
}

// Tracking — номер и ссылка перевозчика из заказа сервера.
type Tracking struct {
	Carrier string  `json:"carrier"`
	Number  *string `json:"number"`
	URL     string  `json:"url"`
}

// BeginOrders сохраняет неизменный запрос до первого внешнего вызова.
func (c *Checkout) BeginOrders(contact Contact, now time.Time) (bool, error) {
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
	if c.state.CartConsumed && c.state.Contact != contact {
		return false, ErrSnapshotChanged
	}
	c.state.Contact = contact
	c.state.Status = StatusCreatingOrders
	for i := range c.state.Groups {
		g := &c.state.Groups[i]
		if g.OrderID != "" || g.Abandoned {
			continue
		}
		if g.QuotePending() {
			return false, ErrSellerUnavailable
		}
		if g.Excluded() {
			if !g.Returnable() {
				g.Status = GroupDeliveryUnavailable
				if g.Quote != nil {
					g.Reason = g.Quote.Reason
				}
			}
			g.CreateExhausted = true
			if !c.state.CartConsumed {
				g.CartReturned = true
			}
		} else {
			g.Status = GroupCreating
		}
	}
	c.rollup()
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
	if !g.Abandoned && g.Status != GroupCreating && g.Status != GroupSellerUnreachable {
		return false, ErrTransition
	}
	total, err := g.Goods.Add(g.DeliveryPrice)
	if err != nil {
		return false, err
	}
	if order.OrderID == "" || order.ExpiresAt.IsZero() || total.Cmp(order.Total) != 0 {
		return false, ErrInvalidOrder
	}
	g.OrderID, g.ExpiresAt = order.OrderID, order.ExpiresAt
	g.NextPollAt = now
	if !g.CancellationConfirmed {
		g.LastOrderStatus = GroupAwaitingPayment
	}
	if g.Abandoned {
		g.ReconciliationRequired = !g.CancellationConfirmed
	} else {
		g.Status, g.Reason = GroupAwaitingPayment, ""
	}
	g.AwaitingSince = now
	c.rollup()
	return true, c.touch(now)
}

// Observe принимает только допустимые переходы. Истечение окна запрещает оплату,
// но отмену подтверждает магазин: он мог получить оплату перед границей окна.
func (c *Checkout) Observe(tenant ids.TenantID, order SellerOrder, next time.Time, now time.Time) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	from := g.Status
	if g.Abandoned || from == GroupSellerUnreachable {
		from = g.LastOrderStatus
	}
	confirmedCancellation := g.CancellationBlocked && order.Status == GroupCancelled
	if g.OrderID == "" || g.OrderID != order.OrderID || (!allowed(from, order.Status) && !confirmedCancellation) {
		return false, ErrTransition
	}
	if order.Status == GroupCancelled && order.Reason == "" {
		return false, ErrInvalidOrder
	}
	if g.Status == order.Status && g.Reason == order.Reason && g.NextPollAt.Equal(next) {
		return false, nil
	}
	if !g.Abandoned {
		g.Status, g.Reason = order.Status, order.Reason
	}
	g.NextPollAt = next
	g.PollFailures = 0
	g.LastErrorCode = ""
	g.LastOrderStatus = order.Status
	if g.CancellationBlocked && (order.Status == GroupDelivered ||
		order.Status == GroupReturned || order.Status == GroupCancelled) {
		g.ReconciliationRequired = false
	}
	c.rollup()
	if order.Status == GroupCancelled && !g.Abandoned && !c.state.PaymentStarted &&
		c.Status() != StatusPaid && c.Status() != StatusPartiallyPaid {
		c.state.Status, c.state.ConfirmedAt = StatusDraft, time.Time{}
	}
	return true, c.touch(now)
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
	if c.Status() == StatusDraft || c.Status() == StatusConfirmed {
		return
	}
	paid, cancelled, pending := 0, 0, 0
	for _, g := range c.state.Groups {
		switch g.EffectiveStatus() {
		case GroupPaid, GroupShipped, GroupDelivered, GroupReturned:
			paid++
		case GroupCancellationRequested, GroupRejected, GroupCancelled,
			GroupPriceChanged, GroupDeliveryUnavailable, GroupUnconfirmed:
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
		for j := range g.Lines {
			g.Lines[j].Discounts = append([]cart.Discount(nil), g.Lines[j].Discounts...)
		}
		if g.Carriers != nil {
			g.Carriers = append([]string{}, g.Carriers...)
		}
		g.Regions = append([]string(nil), g.Regions...)
		if g.Delivery != nil {
			selection := *g.Delivery
			g.Delivery = &selection
		}
		g.Tracking = cloneTracking(g.Tracking)
		g.CurrentPrices = append([]PriceChange(nil), g.CurrentPrices...)
		g.ReturnRemaining = append([]Line(nil), g.ReturnRemaining...)
		for j := range g.ReturnRemaining {
			g.ReturnRemaining[j].Discounts = append([]cart.Discount(nil), g.ReturnRemaining[j].Discounts...)
		}
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

// cloneTracking отделяет указатель номера от снимка заказа.
func cloneTracking(value *Tracking) *Tracking {
	if value == nil {
		return nil
	}
	result := *value
	if value.Number != nil {
		number := *value.Number
		result.Number = &number
	}
	return &result
}

// EffectiveStatus сохраняет последний статус заказа при сбое опроса и исключает окончательный отказ создания.
func (g Group) EffectiveStatus() GroupStatus {
	if g.Abandoned {
		return g.Status
	}
	if g.OrderID == "" && g.CreateExhausted {
		return GroupRejected
	}
	if g.Status == GroupSellerUnreachable && g.OrderID != "" && g.LastOrderStatus != "" {
		return g.LastOrderStatus
	}
	return g.Status
}
