// Package cart — сценарии корзины покупателя.
package cart

import (
	"context"
	"errors"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// allTenants выбирает все группы при перепроверке корзины.
const allTenants ids.TenantID = ""

// ErrSellerUnreachable — магазин не ответил на перепроверку.
var ErrSellerUnreachable = errs.New(errs.Unavailable, "cart.seller_unreachable", "магазин не ответил, попробуйте позже")

// Service — корзина: изменения сверяются с проекцией каталога в PostgreSQL, перед
// оформлением цены и наличие перепроверяются у магазинов. Поиск корзине не нужен.
type Service struct {
	uow      ports.UnitOfWork
	platform ports.PlatformAPI
	ids      ports.IDs
	clock    ports.Clock
	ttl      time.Duration
}

// NewService — сценарии корзины; ttl — срок жизни с последнего изменения.
func NewService(
	uow ports.UnitOfWork,
	platform ports.PlatformAPI,
	identities ports.IDs,
	clock ports.Clock,
	ttl time.Duration,
) *Service {
	return &Service{uow: uow, platform: platform, ids: identities, clock: clock, ttl: ttl}
}

// Get — корзина покупателя; нет или просрочена — пустая.
func (s *Service) Get(ctx context.Context, buyerID ids.UUID) (cart.State, error) {
	var state cart.State
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.Carts().ByBuyer(ctx, buyerID)
		if errors.Is(err, cart.ErrNotFound) {
			state = cart.New(s.ids.NewUUID(), buyerID, s.clock.Now(), s.ttl).State()

			return nil
		}
		if err != nil {
			return err
		}

		if c.ResetIfExpired(s.clock.Now(), s.ttl) {
			if err := tx.Carts().Save(ctx, c); err != nil {
				return err
			}
		}

		state = c.State()

		return nil
	})

	return state, err
}

// Add кладёт предложение в корзину.
func (s *Service) Add(ctx context.Context, buyerID ids.UUID, offerID ids.OfferID, quantity int) (cart.State, error) {
	if quantity == 0 {
		return s.Remove(ctx, buyerID, offerID)
	}

	var offer cart.Offer
	read := func(ctx context.Context, tx ports.Tx) error {
		var err error
		offer, err = s.offer(ctx, tx, offerID)
		return err
	}

	add := func(c *cart.Cart, now time.Time) (bool, error) {
		return true, c.Add(offer, quantity, now, s.ttl)
	}

	return s.change(ctx, buyerID, offerID.Tenant, offerID, false, read, add)
}

// SetQuantity задаёт количество; ноль убирает строку.
func (s *Service) SetQuantity(
	ctx context.Context,
	buyerID ids.UUID,
	offerID ids.OfferID,
	quantity int,
) (cart.State, error) {
	if quantity == 0 {
		return s.Remove(ctx, buyerID, offerID)
	}
	// Недоступное предложение не ошибка: агрегат уменьшит или уберёт строку сам.
	var offer cart.Offer
	read := func(ctx context.Context, tx ports.Tx) error {
		var err error
		offer, err = s.offer(ctx, tx, offerID)
		if errors.Is(err, cart.ErrOfferUnavailable) {
			return nil
		}
		return err
	}

	set := func(c *cart.Cart, now time.Time) (bool, error) {
		return true, c.SetQuantity(offer, quantity, now, s.ttl)
	}

	return s.change(ctx, buyerID, offerID.Tenant, offerID, false, read, set)
}

// Remove убирает строку.
func (s *Service) Remove(ctx context.Context, buyerID ids.UUID, offerID ids.OfferID) (cart.State, error) {
	remove := func(c *cart.Cart, now time.Time) (bool, error) {
		return c.Remove(offerID, now, s.ttl), nil
	}

	return s.change(ctx, buyerID, offerID.Tenant, ids.OfferID{}, true, nil, remove)
}

// Verify перепрашивает у каждого магазина цену и наличие позиций и помечает строки.
func (s *Service) Verify(ctx context.Context, buyerID ids.UUID) (cart.State, error) {
	verify := func(*cart.Cart, time.Time) (bool, error) { return false, nil }

	return s.change(ctx, buyerID, allTenants, ids.OfferID{}, false, nil, verify)
}

func (s *Service) change(
	ctx context.Context,
	buyerID ids.UUID,
	tenant ids.TenantID,
	accepted ids.OfferID,
	removing bool,
	read func(context.Context, ports.Tx) error,
	mutate func(*cart.Cart, time.Time) (bool, error),
) (cart.State, error) {
	var c *cart.Cart
	now := s.clock.Now()
	// Предложение читается в той же транзакции, что и корзина: одним запросом к проекции.
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if read != nil {
			if err := read(ctx, tx); err != nil {
				return err
			}
		}
		var err error
		c, err = tx.Carts().ByBuyer(ctx, buyerID)
		if errors.Is(err, cart.ErrNotFound) {
			c, err = cart.New(s.ids.NewUUID(), buyerID, now, s.ttl), nil
		}
		return err
	})
	if err != nil {
		return cart.State{}, err
	}
	reset := c.ResetIfExpired(now, s.ttl)
	changed, err := mutate(c, now)
	if err != nil {
		return cart.State{}, err
	}
	// Повтор удаления не меняет корзину и не требует доступности магазина.
	if removing && !changed && !reset {
		return c.State(), nil
	}
	var groups []cart.Group
	for _, group := range c.Groups() {
		if tenant == allTenants || group.TenantID == tenant {
			groups = append(groups, group)
		}
	}
	results, err := PriceGroups(ctx, s.platform, groups)
	// Удаление должно работать и при отказе магазина; оставшиеся скидки уже сброшены агрегатом.
	if err != nil && !removing {
		return cart.State{}, err
	}
	for _, line := range c.Lines() {
		if line.Offer != accepted {
			continue
		}
		item, found := ports.ItemCheck{}, false
		for _, check := range results[accepted.Tenant].Items {
			if check.ProductID == accepted.Product {
				item, found = check, true
				break
			}
		}
		if !found || !item.Exists {
			return cart.State{}, cart.ErrOfferUnavailable
		}
		offer := cart.Offer{ID: accepted, Name: line.Name, Price: item.Price, Available: item.Available, Visible: true}
		if err := c.SetQuantity(offer, line.Quantity, now, s.ttl); err != nil {
			return cart.State{}, err
		}
	}
	err = s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for tenant, result := range results {
			pricesChanged := c.Apply(pricingChecks(tenant, result), now, s.ttl)
			promo := cart.PromoCode{}
			if result.PromoCode != nil {
				promo = *result.PromoCode
			}
			promoChanged := c.ApplyPromo(tenant, promo, result.Discounts, now, s.ttl)
			changed = changed || pricesChanged || promoChanged
		}
		// новая корзина без изменений не заводится; просроченная сохраняется очищенной
		if changed || (reset && c.OriginVersion() != 0) {
			return tx.Carts().Save(ctx, c)
		}
		return nil
	})
	return c.State(), err
}

// offer — предложение из проекции каталога, как его видит покупатель сейчас: видимость и цена
// считаются тем же appcatalog.BuyerOffer, что и документ поиска.
func (s *Service) offer(ctx context.Context, tx ports.Tx, offerID ids.OfferID) (cart.Offer, error) {
	state, err := tx.Offers().Offer(ctx, offerID.Tenant, offerID.Product)
	if errors.Is(err, catalog.ErrProductNotFound) {
		return cart.Offer{ID: offerID}, cart.ErrOfferUnavailable
	}
	if err != nil {
		return cart.Offer{}, err
	}
	offer, ok := appcatalog.BuyerOffer(state)
	if !ok || !offer.Visible {
		return cart.Offer{ID: offerID}, cart.ErrOfferUnavailable
	}

	return cart.Offer{
		ID:        offerID,
		Name:      offer.Card.Name,
		Price:     offer.Price,
		Available: cart.MaxQuantity,
		Visible:   true,
	}, nil
}
