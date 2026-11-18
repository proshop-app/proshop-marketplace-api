package checkout

import (
	"slices"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// SetDeliveryOptions сохраняет доступные службы и явный отказ группы без ПВЗ.
func (c *Checkout) SetDeliveryOptions(options, regions map[ids.TenantID][]string, now time.Time) error {
	for i := range c.state.Groups {
		g := &c.state.Groups[i]
		g.Carriers = append([]string{}, options[g.TenantID]...)
		g.Regions = slices.Clone(regions[g.TenantID])
		if len(g.Carriers) == 0 {
			g.Quote = &delivery.Quote{
				Status:    delivery.QuoteUnavailable,
				Reason:    delivery.ReasonCarrierNotConnected,
				ExpiresAt: now,
			}
		}
	}
	return c.touch(now)
}
