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

// CommonCarriers — предварительное правило: одна служба должна обслуживать все оставшиеся магазины.
// Политика собрана здесь; клиент использует результат, не вычисляя своё пересечение.
func (c *Checkout) CommonCarriers() []string {
	common := []string{}
	first := true
	for _, group := range c.state.Groups {
		if group.Abandoned {
			continue
		}
		if first {
			first = false
			common = append([]string{}, group.Carriers...)
		} else {
			common = slices.DeleteFunc(common, func(carrier string) bool {
				return !slices.Contains(group.Carriers, carrier)
			})
		}
		if len(common) == 0 {
			return []string{}
		}
	}
	return common
}
