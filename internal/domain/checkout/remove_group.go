package checkout

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// RemoveGroup меняет состав только по явной команде покупателя и закрывает оплату до нового согласия.
func (c *Checkout) RemoveGroup(tenant ids.TenantID, now time.Time) (bool, error) {
	g := c.group(tenant)
	if g == nil {
		return false, ErrNotFound
	}
	if g.Abandoned {
		return false, nil
	}
	if c.state.PaymentStarted || g.Paid() || c.PaymentComplete() {
		return false, ErrTransition
	}
	g.ReconciliationRequired = !g.CancellationConfirmed &&
		(g.OrderID != "" || g.CreateToken != "" || g.CreateFailures > 0)
	g.Quote = nil
	g.Abandoned, g.CreateExhausted = true, true
	g.Status, g.Reason = GroupCancellationRequested, "buyer_removed"
	if !g.ReconciliationRequired {
		g.Status = GroupCancelled
	}
	if !c.state.CartConsumed {
		g.CartReturned = true
	}
	c.reviewComposition()
	remaining := false
	for _, group := range c.state.Groups {
		remaining = remaining || !group.Excluded()
	}
	if !remaining {
		c.state.Status = StatusCancelled
	}
	return true, c.touch(now)
}
