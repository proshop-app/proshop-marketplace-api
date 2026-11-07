package checkout

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// SetPromoCodes — фиксирует коды в снимке покупки до оформления.
func (c *Checkout) SetPromoCodes(codes map[ids.TenantID]string, now time.Time) (bool, error) {
	if c.state.Status != StatusDraft {
		return false, ErrNotDraft
	}
	changed := false
	for i := range c.state.Groups {
		changed = changed || c.state.Groups[i].PromoCode != codes[c.state.Groups[i].TenantID]
		c.state.Groups[i].PromoCode = codes[c.state.Groups[i].TenantID]
	}
	if !changed {
		return false, nil
	}
	return true, c.touch(now)
}
