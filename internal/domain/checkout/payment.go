package checkout

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// PaymentAmount — остаток единого платежа, включая доставку ожидающих групп.
func (c *Checkout) PaymentAmount() (money.Money, error) {
	if c.state.Status != StatusAwaitingPayment && c.state.Status != StatusPartiallyPaid {
		return money.Money{}, ErrTransition
	}
	var total money.Money
	found := false
	for _, g := range c.state.Groups {
		if g.EffectiveStatus() == GroupCreating || g.EffectiveStatus() == GroupSellerUnreachable {
			return money.Money{}, ErrTransition
		}
		if g.EffectiveStatus() != GroupAwaitingPayment {
			continue
		}
		if g.OrderID == "" {
			return money.Money{}, ErrTransition
		}
		amount, err := g.Goods.Add(g.DeliveryPrice)
		if err != nil {
			return money.Money{}, err
		}
		total, err = total.Add(amount)
		if err != nil {
			return money.Money{}, err
		}
		found = true
	}
	if !found {
		return money.Money{}, ErrTransition
	}
	return total, nil
}

// Pay завершает единый платёж после подтверждения всех ожидающих заказов сервером.
func (c *Checkout) Pay(now time.Time) (bool, error) {
	if c.PaymentComplete() {
		return false, nil
	}
	if _, err := c.PaymentAmount(); err != nil {
		return false, err
	}
	for i := range c.state.Groups {
		g := &c.state.Groups[i]
		if g.EffectiveStatus() != GroupAwaitingPayment {
			continue
		}
		g.Status, g.LastOrderStatus = GroupPaid, GroupPaid
		g.Reason, g.LastErrorCode, g.PollToken = "", "", ""
		g.PollFailures = 0
		g.NextPollAt = now
	}
	c.rollup()
	return true, c.touch(now)
}

// PaymentComplete учитывает отменённые группы: повтор не требует доплаты за них.
func (c *Checkout) PaymentComplete() bool {
	paid := false
	for _, g := range c.state.Groups {
		switch g.EffectiveStatus() {
		case GroupPaid, GroupShipped, GroupDelivered, GroupReturned:
			paid = true
		case GroupCancelled, GroupPriceChanged, GroupDeliveryUnavailable:
		default:
			return false
		}
	}
	return paid
}

// CheckPaymentWindow проверяет все ожидающие заказы до первого внешнего платежа.
func (c *Checkout) CheckPaymentWindow(now time.Time) error {
	for _, g := range c.state.Groups {
		if g.EffectiveStatus() == GroupAwaitingPayment && !now.Before(g.ExpiresAt) {
			return ErrPaymentExpired
		}
	}
	return nil
}
