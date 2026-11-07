package cart

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Ограничение защищает сервер от всплеска запросов корзины с множеством магазинов.
const pricingConcurrency = 8

// SetPromo сохраняет код группы или удаляет его при пустом значении.
func (s *Service) SetPromo(ctx context.Context, buyer ids.UUID, tenant ids.TenantID, code string) (cart.State, error) {
	removing := strings.TrimSpace(code) == ""
	return s.change(ctx, buyer, tenant, ids.OfferID{}, removing, func(c *cart.Cart, now time.Time) (bool, error) {
		return c.SetPromo(tenant, code, now, s.ttl)
	})
}

// PriceGroups получает единый снимок цены и скидок вне транзакции.
func PriceGroups(
	ctx context.Context,
	platform ports.PlatformAPI,
	buyer ids.UUID,
	groups []cart.Group,
) (map[ids.TenantID]ports.GroupPricing, error) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var failed error
	results := make(map[ids.TenantID]ports.GroupPricing, len(groups))
	limit := make(chan struct{}, pricingConcurrency)
	for _, group := range groups {
		wg.Add(1)
		go func(group cart.Group) {
			defer wg.Done()
			message := "магазин " + string(group.TenantID) + " не ответил, попробуйте позже"
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				failed = ErrSellerUnreachable.WithMessage(message).Wrap(ctx.Err())
				mu.Unlock()
				return
			}
			defer func() { <-limit }()
			request := ports.GroupPricingRequest{BuyerID: buyer}
			if group.PromoCode != nil {
				request.PromoCode = group.PromoCode.Code
			}
			for _, line := range group.Lines {
				request.Items = append(request.Items, ports.ItemRequest{
					ProductID: line.Offer.Product,
					Quantity:  line.Quantity,
				})
			}
			result, err := platform.PriceGroup(ctx, group.TenantID, request)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = ErrSellerUnreachable.WithMessage(message).Wrap(err)
				return
			}
			results[group.TenantID] = result
		}(group)
	}
	wg.Wait()
	return results, failed
}

func pricingChecks(tenant ids.TenantID, result ports.GroupPricing) []cart.Check {
	checks := make([]cart.Check, 0, len(result.Items))
	for _, item := range result.Items {
		checks = append(checks, cart.Check{
			Offer:     ids.OfferID{Tenant: tenant, Product: item.ProductID},
			Exists:    item.Exists,
			Price:     item.Price,
			Available: item.Available,
		})
	}
	return checks
}
