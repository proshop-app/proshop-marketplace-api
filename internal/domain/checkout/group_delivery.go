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

// SelectDeliveries атомарно применяет общий ПВЗ выбранным группам покупки.
func (c *Checkout) SelectDeliveries(
	selection Selection,
	quotes map[ids.TenantID]delivery.Quote,
	now time.Time,
) error {
	if c.state.Status != StatusDraft {
		return ErrNotDraft
	}
	for tenant := range quotes {
		if group := c.group(tenant); group == nil {
			return ErrNotFound
		} else if group.Status != "" || group.Abandoned {
			return ErrSnapshotChanged
		}
	}
	for tenant, quote := range quotes {
		group := c.group(tenant)
		selected := selection
		group.Delivery, group.Quote = &selected, &quote
	}
	c.state.Delivery = &selection
	return c.touch(now)
}

// Selection возвращает снимок группы; общий выбор нужен для сохранённых ранее покупок.
func (g Group) Selection(fallback *Selection) *Selection {
	if g.Delivery != nil {
		return g.Delivery
	}
	if g.Carriers != nil {
		return nil
	}
	return fallback
}
