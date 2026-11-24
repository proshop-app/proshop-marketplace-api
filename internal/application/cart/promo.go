package cart

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/safego"
)

// Ограничение защищает сервер от всплеска запросов корзины с множеством магазинов.
const pricingConcurrency = 8

// SetPromo сохраняет код группы или удаляет его при пустом значении.
func (s *Service) SetPromo(ctx context.Context, buyer ids.UUID, tenant ids.TenantID, code string) (cart.State, error) {
	removing := strings.TrimSpace(code) == ""
	promo := func(c *cart.Cart, now time.Time) (bool, error) {
		return c.SetPromo(tenant, code, now, s.ttl)
	}
	return s.change(ctx, cartChange{buyer: buyer, tenant: tenant, removing: removing, mutate: promo})
}

// PriceGroups получает единый снимок цены и скидок вне транзакции.
func PriceGroups(
	ctx context.Context,
	platform ports.PlatformAPI,
	groups []cart.Group,
) (map[ids.TenantID]ports.GroupPricing, error) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var failed error
	results := make(map[ids.TenantID]ports.GroupPricing, len(groups))
	limit := make(chan struct{}, pricingConcurrency)
	for _, group := range groups {
		wg.Add(1)
		var result ports.GroupPricing
		safego.Run(
			"cart.price-group",
			func() error {
				var err error
				result, err = priceGroup(ctx, platform, group, limit)
				return err
			},
			func(err error) {
				defer wg.Done()
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					message := "магазин " + string(group.TenantID) + " не ответил, попробуйте позже"
					failed = ErrSellerUnreachable.WithMessage(message).Wrap(err)
					return
				}
				results[group.TenantID] = result
			},
		)
	}
	wg.Wait()
	return results, failed
}

// priceGroup спрашивает цену группы, заняв место из общего предела одновременных вызовов.
func priceGroup(
	ctx context.Context,
	platform ports.PlatformAPI,
	group cart.Group,
	limit chan struct{},
) (ports.GroupPricing, error) {
	select {
	case limit <- struct{}{}:
	case <-ctx.Done():
		return ports.GroupPricing{}, ctx.Err()
	}
	defer func() { <-limit }()
	request := ports.GroupPricingRequest{}
	if group.PromoCode != nil {
		request.PromoCode = group.PromoCode.Code
	}
	for _, line := range group.Lines {
		request.Items = append(request.Items, ports.ItemRequest{
			ProductID: line.Offer.Product,
			Quantity:  line.Quantity,
		})
	}
	return platform.PriceGroup(ports.WithPlatformCallClass(ctx, ports.PlatformCheckout), group.TenantID, request)
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
