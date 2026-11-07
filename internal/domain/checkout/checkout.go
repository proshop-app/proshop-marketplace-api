// Package checkout — покупка: неизменяемый снимок корзины по продавцам,
// выбранная доставка и независимые заказы магазинов.
package checkout

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_:.-]{8,128}$`)

// Status — состояние покупки.
type Status string

const (
	// StatusDraft — покупка собрана из корзины, доставка выбирается.
	StatusDraft Status = "draft"
	// StatusConfirmed — покупатель подтвердил: цены и доставка перепроверены у магазинов.
	StatusConfirmed Status = "confirmed"
	// StatusCreatingOrders — создаются заказы магазинов.
	StatusCreatingOrders Status = "creating_orders"
	// StatusAwaitingPayment — все созданные группы ждут оплаты.
	StatusAwaitingPayment Status = "awaiting_payment"
	// StatusPartiallyPaid — оплачена часть магазинов, остальные независимы.
	StatusPartiallyPaid Status = "partially_paid"
	// StatusPaid — оплачены все группы.
	StatusPaid Status = "paid"
	// StatusCancelled — все группы отменены или требуют нового оформления.
	StatusCancelled Status = "cancelled"
)

// Line — позиция снимка.
type Line struct {
	Discounts []cart.Discount
	Offer     ids.OfferID
	Name      string
	Quantity  int
	Price     money.Money
}

// Group — заказ одного продавца в покупке.
type Group struct {
	PromoCode string
	Tracking  *Tracking
	TenantID  ids.TenantID
	// ExternalCheckoutID — ключ заказа у магазина: <checkoutId>:<tenantId>.
	ExternalCheckoutID string
	Lines              []Line
	Goods              money.Money
	// Quote — расчёт доставки до выбранного пункта; нет — нужно посчитать.
	Quote           *delivery.Quote
	Status          GroupStatus
	OrderID         string
	PaymentURL      string
	ExpiresAt       time.Time
	PaymentOrder    int
	DeliveryPrice   money.Money
	Reason          string
	NextPollAt      time.Time
	AwaitingSince   time.Time
	CartReturned    bool
	CurrentPrices   []PriceChange
	LastErrorCode   string
	CreateFailures  int
	CreateExhausted bool
	NextCreateAt    time.Time
	CreateToken     string
	PollFailures    int
	PollToken       string
	LastOrderStatus GroupStatus
	ReturnFailed    bool
	ReturnReason    string
	ReturnRemaining []Line
	NextReturnAt    time.Time
}

// Selection — выбранная доставка.
type Selection struct {
	Carrier         delivery.Carrier
	PickupPointCode string
	City            string
	Address         string
	RegionCode      string
}

// PriceCheck — ответ магазина по позиции при подтверждении.
type PriceCheck struct {
	Discounts []cart.Discount
	Exists    bool
	Price     money.Money
	Available int
}

// State — плоский снимок покупки.
type State struct {
	ID             ids.UUID
	BuyerID        ids.UUID
	IdempotencyKey string
	Status         Status
	Groups         []Group
	Delivery       *Selection
	GoodsTotal     money.Money
	DeliveryTotal  money.Money
	Total          money.Money
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ConfirmedAt    time.Time
	Contact        Contact
	ReturnURL      string
	CartConsumed   bool
}

// Checkout — покупка покупателя.
type Checkout struct {
	state  State
	origin struct {
		status  Status
		version int64
	}
}

// ParseIdempotencyKey проверяет ключ идемпотентности создания.
func ParseIdempotencyKey(value string) (string, error) {
	key := strings.TrimSpace(value)
	if !idempotencyKeyPattern.MatchString(key) {
		return "", ErrInvalidIdempotencyKey
	}

	return key, nil
}

// New собирает покупку из позиций корзины, сгруппированных по продавцам.
func New(id, buyerID ids.UUID, key string, lines []Line, now time.Time) (*Checkout, error) {
	if len(lines) == 0 {
		return nil, ErrEmpty
	}

	byTenant := map[ids.TenantID]*Group{}

	for _, line := range lines {
		if line.Quantity < 1 || line.Price.IsNegative() {
			return nil, ErrEmpty.WithMessage("в корзине позиция без количества или с отрицательной ценой")
		}

		group, ok := byTenant[line.Offer.Tenant]
		if !ok {
			group = &Group{
				TenantID:           line.Offer.Tenant,
				ExternalCheckoutID: string(id) + ":" + string(line.Offer.Tenant),
			}
			byTenant[line.Offer.Tenant] = group
		}

		group.Lines = append(group.Lines, line)
	}

	c := &Checkout{state: State{
		ID:             id,
		BuyerID:        buyerID,
		IdempotencyKey: key,
		Status:         StatusDraft,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}}

	for _, group := range byTenant {
		c.state.Groups = append(c.state.Groups, *group)
	}

	sort.Slice(c.state.Groups, func(i, j int) bool { return c.state.Groups[i].TenantID < c.state.Groups[j].TenantID })

	if err := c.recount(); err != nil {
		return nil, err
	}

	c.state = cloneState(c.state)
	return c, nil
}

// Restore поднимает покупку из хранилища.
func Restore(state State) *Checkout {
	c := &Checkout{state: cloneState(state)}
	c.origin.status, c.origin.version = state.Status, state.Version

	return c
}

// State — снимок для хранения.
func (c *Checkout) State() State {
	return cloneState(c.state)
}

// Origin — статус и версия на момент чтения; у новой покупки версия нулевая.
func (c *Checkout) Origin() (Status, int64) { return c.origin.status, c.origin.version }

// MarkSaved фиксирует сохранённое состояние.
func (c *Checkout) MarkSaved() { c.origin.status, c.origin.version = c.state.Status, c.state.Version }

// ID — покупка.
func (c *Checkout) ID() ids.UUID { return c.state.ID }

// BuyerID — покупатель.
func (c *Checkout) BuyerID() ids.UUID { return c.state.BuyerID }

// Status — состояние.
func (c *Checkout) Status() Status { return c.state.Status }

// Groups — заказы продавцов.
func (c *Checkout) Groups() []Group { return cloneState(c.state).Groups }

// Delivery — выбранная доставка; nil — не выбрана.
func (c *Checkout) Delivery() *Selection { return c.State().Delivery }

// SelectDelivery запоминает пункт выдачи и расчёты всех продавцов.
func (c *Checkout) SelectDelivery(selection Selection, quotes map[ids.TenantID]delivery.Quote, now time.Time) error {
	if c.state.Status != StatusDraft {
		return ErrNotDraft
	}
	if len(quotes) != len(c.state.Groups) {
		return ErrInvalidQuotes
	}

	for i := range c.state.Groups {
		quote, ok := quotes[c.state.Groups[i].TenantID]
		if !ok {
			return ErrInvalidQuotes
		}

		c.state.Groups[i].Quote = &quote
	}

	c.state.Delivery = &selection

	return c.touch(now)
}

// NeedsQuote — продавцы, чей расчёт нужно получить заново: его нет, цена
// доставки устарела или магазин не ответил. Без выбранной доставки — никто.
func (c *Checkout) NeedsQuote(now time.Time) []ids.TenantID {
	if c.state.Delivery == nil {
		return nil
	}

	var tenants []ids.TenantID

	for _, group := range c.state.Groups {
		quote := group.Quote

		switch {
		case quote == nil,
			quote.Status == delivery.QuoteAvailable && quote.Expired(now),
			quote.Status == delivery.QuoteUnavailable && quote.Reason == delivery.ReasonSellerUnreachable:
			tenants = append(tenants, group.TenantID)
		}
	}

	return tenants
}

// Undeliverable — продавцы, которые ответили, что в выбранный пункт не везут.
// Их не исправит ни перепроверка цен, ни пересчёт: нужен другой пункт.
func (c *Checkout) Undeliverable() []ids.TenantID {
	var tenants []ids.TenantID

	for _, group := range c.state.Groups {
		if q := group.Quote; q != nil && q.Status == delivery.QuoteUnavailable &&
			q.Reason != delivery.ReasonSellerUnreachable {
			tenants = append(tenants, group.TenantID)
		}
	}

	return tenants
}

// Requote подменяет расчёты указанных продавцов.
func (c *Checkout) Requote(quotes map[ids.TenantID]delivery.Quote, now time.Time) error {
	if c.state.Status != StatusDraft {
		return ErrNotDraft
	}

	for i := range c.state.Groups {
		if quote, ok := quotes[c.state.Groups[i].TenantID]; ok {
			c.state.Groups[i].Quote = &quote
		}
	}

	return c.touch(now)
}

// Reprice применяет ответы магазинов по позициям. Пропавшая или нехватающая
// позиция — ErrOfferUnavailable без изменений. Новая цена записывается, а
// расчёт доставки этого продавца сбрасывается: стоимость доставки у магазина
// может зависеть от суммы товаров. Возвращает, изменилось ли что-нибудь.
func (c *Checkout) Reprice(checks map[ids.OfferID]PriceCheck, now time.Time) (bool, error) {
	if c.state.Status != StatusDraft {
		return false, ErrNotDraft
	}

	var missing []string

	for _, group := range c.state.Groups {
		for _, line := range group.Lines {
			check, ok := checks[line.Offer]
			if ok {
				if _, err := cart.DiscountedAmount(check.Price, line.Quantity, check.Discounts); err != nil {
					return false, ErrOfferUnavailable.Wrap(err)
				}
			}
			if !ok || !check.Exists || check.Available < line.Quantity {
				missing = append(missing, line.Offer.String())
			}
		}
	}

	if len(missing) > 0 {
		return false, ErrOfferUnavailable.WithMessage("недоступны: " + strings.Join(missing, ", "))
	}

	changed := false

	for gi := range c.state.Groups {
		group := &c.state.Groups[gi]
		groupChanged := false

		for li := range group.Lines {
			line := &group.Lines[li]
			check := checks[line.Offer]
			if check.Price != line.Price || !slices.Equal(check.Discounts, line.Discounts) {
				line.Price = check.Price
				line.Discounts = append([]cart.Discount(nil), check.Discounts...)
				groupChanged = true
			}
		}

		if groupChanged {
			group.Quote = nil
			changed = true
		}
	}

	if !changed {
		return false, nil
	}

	return true, c.touch(now)
}

// Confirm подтверждает покупку: доставка выбрана, все продавцы везут в пункт,
// цены доставки действуют.
func (c *Checkout) Confirm(now time.Time) error {
	if c.state.Status != StatusDraft {
		return ErrNotDraft
	}
	if c.state.Delivery == nil {
		return ErrDeliveryNotSelected
	}

	var unavailable []string

	for _, group := range c.state.Groups {
		switch {
		case group.Quote == nil:
			return ErrQuoteExpired
		case group.Quote.Status != delivery.QuoteAvailable:
			unavailable = append(unavailable, string(group.TenantID))
		case group.Quote.Expired(now):
			return ErrQuoteExpired
		}
	}

	if len(unavailable) > 0 {
		return ErrDeliveryUnavailable.WithMessage("не доставляют в выбранный пункт: " + strings.Join(unavailable, ", "))
	}

	c.state.Status = StatusConfirmed
	c.state.ConfirmedAt = now

	return c.touch(now)
}

func (c *Checkout) touch(now time.Time) error {
	if err := c.recount(); err != nil {
		return err
	}

	c.state.Version = c.origin.version + 1
	c.state.UpdatedAt = now

	return nil
}

// recount пересчитывает суммы групп и итоги сложением через Money.
func (c *Checkout) recount() error {
	goodsTotal, deliveryTotal := money.Money{}, money.Money{}

	for i := range c.state.Groups {
		group := &c.state.Groups[i]
		goods := money.Money{}

		for _, line := range group.Lines {
			amount, err := cart.DiscountedAmount(line.Price, line.Quantity, line.Discounts)
			if err != nil {
				return err
			}

			if goods, err = goods.Add(amount); err != nil {
				return err
			}
		}

		group.Goods = goods
		group.DeliveryPrice = money.Money{}

		var err error
		if goodsTotal, err = goodsTotal.Add(goods); err != nil {
			return err
		}

		if group.Quote != nil && group.Quote.Status == delivery.QuoteAvailable {
			group.DeliveryPrice = group.Quote.BuyerCost
			if deliveryTotal, err = deliveryTotal.Add(group.DeliveryPrice); err != nil {
				return err
			}
		}
	}

	total, err := goodsTotal.Add(deliveryTotal)
	if err != nil {
		return err
	}

	c.state.GoodsTotal, c.state.DeliveryTotal, c.state.Total = goodsTotal, deliveryTotal, total

	return nil
}
