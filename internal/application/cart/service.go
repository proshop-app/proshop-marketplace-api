// Package cart — сценарии корзины покупателя.
package cart

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// ErrSellerUnreachable — магазин не ответил на перепроверку.
var ErrSellerUnreachable = errs.New(errs.Unavailable, "cart.seller_unreachable", "магазин не ответил, попробуйте позже")

// Service — корзина: изменения сверяются с проекцией каталога, перед
// оформлением цены и наличие перепроверяются у магазинов.
type Service struct {
	uow      ports.UnitOfWork
	index    ports.OfferIndex
	platform ports.PlatformAPI
	ids      ports.IDs
	clock    ports.Clock
	ttl      time.Duration
}

// NewService — сценарии корзины; ttl — срок жизни с последнего изменения.
func NewService(
	uow ports.UnitOfWork,
	index ports.OfferIndex,
	platform ports.PlatformAPI,
	identities ports.IDs,
	clock ports.Clock,
	ttl time.Duration,
) *Service {
	return &Service{uow: uow, index: index, platform: platform, ids: identities, clock: clock, ttl: ttl}
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
	offer, err := s.offer(ctx, offerID)
	if err != nil {
		return cart.State{}, err
	}

	return s.change(ctx, buyerID, func(c *cart.Cart, now time.Time) (bool, error) {
		return true, c.Add(offer, quantity, now, s.ttl)
	})
}

// SetQuantity задаёт количество; ноль убирает строку.
func (s *Service) SetQuantity(
	ctx context.Context,
	buyerID ids.UUID,
	offerID ids.OfferID,
	quantity int,
) (cart.State, error) {
	offer, err := s.offer(ctx, offerID)
	if err != nil && !errors.Is(err, cart.ErrOfferUnavailable) {
		return cart.State{}, err
	}

	return s.change(ctx, buyerID, func(c *cart.Cart, now time.Time) (bool, error) {
		if quantity == 0 {
			return c.Remove(offerID, now, s.ttl), nil
		}

		return true, c.SetQuantity(offer, quantity, now, s.ttl)
	})
}

// Remove убирает строку.
func (s *Service) Remove(ctx context.Context, buyerID ids.UUID, offerID ids.OfferID) (cart.State, error) {
	return s.change(ctx, buyerID, func(c *cart.Cart, now time.Time) (bool, error) {
		return c.Remove(offerID, now, s.ttl), nil
	})
}

// Verify перепрашивает у каждого магазина цену и наличие позиций и помечает строки.
func (s *Service) Verify(ctx context.Context, buyerID ids.UUID) (cart.State, error) {
	current, err := s.Get(ctx, buyerID)
	if err != nil || len(current.Lines) == 0 {
		return current, err
	}

	checks, err := CheckGroups(ctx, s.platform, cart.Restore(current).Groups())
	if err != nil {
		return cart.State{}, err
	}

	return s.change(ctx, buyerID, func(c *cart.Cart, now time.Time) (bool, error) {
		return c.Apply(checks, now, s.ttl), nil
	})
}

// CheckGroups спрашивает магазины параллельно, по запросу на магазин.
func CheckGroups(ctx context.Context, platform ports.PlatformAPI, groups []cart.Group) ([]cart.Check, error) {
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		checks []cart.Check
		failed error
	)

	for _, group := range groups {
		wg.Add(1)

		go func(group cart.Group) {
			defer wg.Done()

			items := make([]ports.ItemRequest, 0, len(group.Lines))
			for _, line := range group.Lines {
				items = append(items, ports.ItemRequest{ProductID: line.Offer.Product, Quantity: line.Quantity})
			}

			answers, err := platform.CheckItems(ctx, group.TenantID, items)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				message := "магазин " + string(group.TenantID) + " не ответил, попробуйте позже"
				failed = ErrSellerUnreachable.WithMessage(message).Wrap(err)

				return
			}

			byProduct := make(map[ids.ProductID]ports.ItemCheck, len(answers))
			for _, answer := range answers {
				byProduct[answer.ProductID] = answer
			}

			for _, line := range group.Lines {
				answer, ok := byProduct[line.Offer.Product]
				checks = append(
					checks,
					cart.Check{
						Offer:     line.Offer,
						Exists:    ok && answer.Exists,
						Price:     answer.Price,
						Available: answer.Available,
					},
				)
			}
		}(group)
	}

	wg.Wait()

	return checks, failed
}

func (s *Service) change(
	ctx context.Context,
	buyerID ids.UUID,
	mutate func(*cart.Cart, time.Time) (bool, error),
) (cart.State, error) {
	var state cart.State

	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		now := s.clock.Now()

		c, err := tx.Carts().ByBuyer(ctx, buyerID)
		if errors.Is(err, cart.ErrNotFound) {
			c, err = cart.New(s.ids.NewUUID(), buyerID, now, s.ttl), nil
		}
		if err != nil {
			return err
		}

		reset := c.ResetIfExpired(now, s.ttl)

		changed, err := mutate(c, now)
		if err != nil {
			return err
		}

		// новая корзина без изменений не заводится; просроченная сохраняется очищенной
		if changed || (reset && c.OriginVersion() != 0) {
			if err := tx.Carts().Save(ctx, c); err != nil {
				return err
			}
		}

		state = c.State()

		return nil
	})

	return state, err
}

// offer — предложение из индекса каталога, как его видит покупатель сейчас.
func (s *Service) offer(ctx context.Context, offerID ids.OfferID) (cart.Offer, error) {
	doc, found, err := s.index.Get(ctx, offerID.String())
	if err != nil {
		return cart.Offer{}, err
	}
	if !found || !doc.Visible {
		return cart.Offer{ID: offerID}, cart.ErrOfferUnavailable
	}

	return cart.Offer{
		ID:        offerID,
		Name:      doc.Name,
		Price:     money.FromKopecks(doc.Price),
		Available: doc.Available,
		Visible:   true,
	}, nil
}
