package checkout

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"

	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// PaymentResult различает подтверждённую оплату и доказанное отсутствие отправленной команды.
type PaymentResult struct {
	Paid   map[ids.TenantID]string
	Unpaid map[ids.TenantID]bool
}

// PaymentAmount — остаток единого платежа, включая доставку ожидающих групп.
func (c *Checkout) PaymentAmount() (money.Money, error) {
	if c.state.Status != StatusAwaitingPayment && c.state.Status != StatusPartiallyPaid {
		return money.Money{}, ErrTransition
	}
	if c.NeedsConfirmation() {
		return money.Money{}, ErrDeliveryChanged
	}
	var total money.Money
	found, held, expired := false, false, false
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
		if g.PaymentHold == HoldPaymentExpired {
			expired = true
			continue
		}
		if g.PaymentHold != "" {
			held = true
			continue
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
		if expired {
			return money.Money{}, ErrPaymentExpired
		}
		if held {
			return money.Money{}, ErrPaymentPaused
		}
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
		if !g.ReadyForPayment() {
			continue
		}
		g.Status, g.LastOrderStatus = GroupPaid, GroupPaid
		g.Reason, g.LastErrorCode, g.PollToken = "", "", ""
		g.PollFailures = 0
		g.NextPollAt = time.Time{}
		g.OrderStatusAt = now
	}
	c.rollup()
	return true, c.touch(now)
}

// PaymentComplete учитывает отменённые группы: повтор не требует доплаты за них.
func (c *Checkout) PaymentComplete() bool {
	paid := false
	for _, g := range c.state.Groups {
		if g.Paid() {
			paid = true
			continue
		}
		switch g.EffectiveStatus() {
		case GroupCancellationRequested, GroupRejected, GroupCancelled,
			GroupPriceChanged, GroupDeliveryUnavailable, GroupUnconfirmed:
		default:
			return false
		}
	}
	return paid
}

// CheckPaymentWindow проверяет все ожидающие заказы до первого внешнего платежа.
func (c *Checkout) CheckPaymentWindow(now time.Time) error {
	for _, g := range c.state.Groups {
		if g.ReadyForPayment() && !now.Before(g.ExpiresAt) {
			return ErrPaymentExpired
		}
	}
	return nil
}

// Paid означает состоявшийся платёж, независимо от последующего исполнения.
func (g Group) Paid() bool {
	return paidStatus(g.EffectiveStatus()) || paidStatus(g.LastOrderStatus)
}

// HasPaidGroups сохраняет состоявшиеся деньги независимо от текущего общего статуса.
func (c *Checkout) HasPaidGroups() bool {
	for _, g := range c.state.Groups {
		if g.Paid() {
			return true
		}
	}
	return false
}

// ReadyForPayment исключает только приостановленную группу из текущего платежа.
func (g Group) ReadyForPayment() bool {
	return !g.Excluded() && g.EffectiveStatus() == GroupAwaitingPayment && g.OrderID != "" && g.PaymentHold == ""
}

// PaymentOrders фиксирует состав отправляемой оплаты после актуальной проверки готовности.
func (c *Checkout) PaymentOrders() map[ids.TenantID]string {
	orders := make(map[ids.TenantID]string)
	for _, g := range c.state.Groups {
		if g.ReadyForPayment() {
			orders[g.TenantID] = g.OrderID
		}
	}
	return orders
}

// reviewComposition требует подтверждения только ещё не оплаченного состава.
func (c *Checkout) reviewComposition() {
	c.state.ConfirmedAt = time.Time{}
	if c.HasPaidGroups() || c.state.PaymentStarted {
		c.rollup()
	} else {
		c.state.Status = StatusDraft
	}
}

func (c *Checkout) paymentPending() bool {
	for _, g := range c.state.Groups {
		if g.PaymentPending {
			return true
		}
	}
	return false
}

func paidStatus(status GroupStatus) bool {
	switch status {
	case GroupPaid, GroupAssembled, GroupShipped, GroupAwaitingPickup, GroupDelivered, GroupReturned:
		return true
	default:
		return false
	}
}

// NeedsConfirmation сохраняет требование согласия на новый состав после частичной оплаты.
// Общий статус отражает уже принятые деньги, поэтому не переводится в draft.
func (c *Checkout) NeedsConfirmation() bool {
	return c.Status() == StatusDraft || c.NeedsOrderConfirmation()
}

// NeedsOrderConfirmation требует согласия на изменившийся состав уже начатой покупки.
// Неизвестный исход оплаты сохраняет заказы, даже если оплаченных групп пока нет.
func (c *Checkout) NeedsOrderConfirmation() bool {
	return (c.HasPaidGroups() || c.state.CartConsumed) && c.state.ConfirmedAt.IsZero() && !c.PaymentComplete()
}
