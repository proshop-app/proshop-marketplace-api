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

// cartChange — одно изменение корзины: чья, какой магазин перепроверить, какое предложение принято,
// удаление ли это, что прочитать в транзакции корзины и как изменить агрегат.
type cartChange struct {
	operation *cart.AddOperation
	buyer     ids.UUID
	tenant    ids.TenantID
	accepted  ids.OfferID
	removing  bool
	read      func(context.Context, ports.Tx) error
	mutate    func(*cart.Cart, time.Time) (bool, error)
}

// Service — корзина: изменения сверяются с проекцией каталога в PostgreSQL, перед
// оформлением цены и наличие перепроверяются у магазинов. Поиск корзине не нужен.
type Service struct {
	uow          ports.UnitOfWork
	platform     ports.PlatformAPI
	ids          ports.IDs
	clock        ports.Clock
	ttl          time.Duration
	addKeySecret []byte
}

// NewService — сценарии корзины; ttl — срок жизни с последнего изменения.
func NewService(
	uow ports.UnitOfWork,
	platform ports.PlatformAPI,
	identities ports.IDs,
	clock ports.Clock,
	ttl time.Duration,
	addKeySecret string,
) *Service {
	return &Service{
		uow:          uow,
		platform:     platform,
		ids:          identities,
		clock:        clock,
		ttl:          ttl,
		addKeySecret: []byte(addKeySecret),
	}
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

func (s *Service) add(
	ctx context.Context,
	buyerID ids.UUID,
	offerID ids.OfferID,
	quantity int,
	operation cart.AddOperation,
) (cart.State, error) {
	var offer cart.Offer
	read := func(ctx context.Context, tx ports.Tx) error {
		var err error
		offer, err = s.offer(ctx, tx, offerID)
		return err
	}

	add := func(c *cart.Cart, now time.Time) (bool, error) {
		return true, c.Add(offer, quantity, now, s.ttl)
	}

	return s.change(ctx, cartChange{
		buyer:     buyerID,
		tenant:    offerID.Tenant,
		accepted:  offerID,
		read:      read,
		mutate:    add,
		operation: &operation,
	})
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

	return s.change(ctx, cartChange{buyer: buyerID, tenant: offerID.Tenant, accepted: offerID, read: read, mutate: set})
}

// Remove убирает строку.
func (s *Service) Remove(ctx context.Context, buyerID ids.UUID, offerID ids.OfferID) (cart.State, error) {
	remove := func(c *cart.Cart, now time.Time) (bool, error) {
		return c.Remove(offerID, now, s.ttl), nil
	}

	return s.change(ctx, cartChange{buyer: buyerID, tenant: offerID.Tenant, removing: true, mutate: remove})
}

// Verify перепрашивает у каждого магазина цену и наличие позиций и помечает строки.
func (s *Service) Verify(ctx context.Context, buyerID ids.UUID) (cart.State, error) {
	verify := func(*cart.Cart, time.Time) (bool, error) { return false, nil }

	return s.change(ctx, cartChange{buyer: buyerID, tenant: allTenants, mutate: verify})
}

// change проводит изменение корзины: чтение корзины и предложения, изменение агрегата, цены
// магазинов, сверка принятого предложения и сохранение.
func (s *Service) change(ctx context.Context, request cartChange) (cart.State, error) {
	now := s.clock.Now()
	c, replayed, err := s.load(ctx, request, now)
	if err != nil {
		return cart.State{}, err
	}
	if replayed {
		if c.Expired(now) {
			c = cart.New(s.ids.NewUUID(), request.buyer, now, s.ttl)
		}
		return c.State(), nil
	}
	state, err := s.applyChange(ctx, c, request, now)
	if err != nil && request.operation != nil {
		s.releaseAdd(ctx, *request.operation)
	}
	return state, err
}

func (s *Service) applyChange(
	ctx context.Context,
	c *cart.Cart,
	request cartChange,
	now time.Time,
) (cart.State, error) {
	reset := c.ResetIfExpired(now, s.ttl)
	changed, err := request.mutate(c, now)
	if err != nil {
		return cart.State{}, err
	}
	// Повтор удаления не меняет корзину и не требует доступности магазина.
	if request.removing && !changed && !reset {
		return c.State(), nil
	}
	results, err := PriceGroups(ctx, s.platform, groupsOf(c, request.tenant))
	// Удаление должно работать и при отказе магазина; оставшиеся скидки уже сброшены агрегатом.
	if err != nil && !request.removing {
		return cart.State{}, err
	}
	if err := s.acceptOffer(c, request.accepted, results, now); err != nil {
		return cart.State{}, err
	}
	// Цены и скидки магазинов меняют агрегат при сохранении: состояние читается после него.
	err = s.save(ctx, c, results, changed, reset, now, request.operation)
	return c.State(), err
}

// load читает корзину покупателя и, если нужно, предложение — в одной транзакции.
func (s *Service) load(ctx context.Context, request cartChange, now time.Time) (*cart.Cart, bool, error) {
	var c *cart.Cart
	var replayed bool
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if request.operation != nil {
			var err error
			replayed, err = tx.Carts().ClaimAdd(ctx, *request.operation)
			if err != nil {
				return err
			}
		}
		if request.read != nil && !replayed {
			if err := request.read(ctx, tx); err != nil {
				return err
			}
		}
		var err error
		c, err = tx.Carts().ByBuyer(ctx, request.buyer)
		if errors.Is(err, cart.ErrNotFound) {
			c, err = cart.New(s.ids.NewUUID(), request.buyer, now, s.ttl), nil
		}
		return err
	})
	return c, replayed, err
}

// acceptOffer сверяет только что принятое предложение с ответом магазина: цена и остаток — его.
func (s *Service) acceptOffer(
	c *cart.Cart,
	accepted ids.OfferID,
	results map[ids.TenantID]ports.GroupPricing,
	now time.Time,
) error {
	for _, line := range c.Lines() {
		if line.Offer != accepted {
			continue
		}
		item, found := itemCheck(results[accepted.Tenant], accepted.Product)
		if !found || !item.Exists {
			return cart.ErrOfferUnavailable
		}
		offer := cart.Offer{ID: accepted, Name: line.Name, Price: item.Price, Available: item.Available, Visible: true}
		if err := c.SetQuantity(offer, line.Quantity, now, s.ttl); err != nil {
			return err
		}
	}
	return nil
}

// save применяет цены и скидки магазинов и сохраняет корзину, если она изменилась.
func (s *Service) save(
	ctx context.Context,
	c *cart.Cart,
	results map[ids.TenantID]ports.GroupPricing,
	changed, reset bool,
	now time.Time,
	operation *cart.AddOperation,
) error {
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if operation != nil {
			if err := tx.Carts().CompleteAdd(ctx, *operation); err != nil {
				return err
			}
		}
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
}

// groupsOf — группы магазина изменения или все группы при перепроверке корзины.
func groupsOf(c *cart.Cart, tenant ids.TenantID) []cart.Group {
	var groups []cart.Group
	for _, group := range c.Groups() {
		if tenant == allTenants || group.TenantID == tenant {
			groups = append(groups, group)
		}
	}
	return groups
}

func itemCheck(result ports.GroupPricing, product ids.ProductID) (ports.ItemCheck, bool) {
	for _, check := range result.Items {
		if check.ProductID == product {
			return check, true
		}
	}
	return ports.ItemCheck{}, false
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
