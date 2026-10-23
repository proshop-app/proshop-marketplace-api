package fake

import (
	"context"
	"sort"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type buyers struct{ d *data }

type challenges struct{ d *data }

type carts struct{ d *data }

type checkouts struct{ d *data }

type pickupPoints struct{ d *data }

func (r buyers) FindByPhone(_ context.Context, phone buyer.Phone) (buyer.Buyer, error) {
	for _, b := range r.d.buyers {
		if b.Phone == phone {
			return b, nil
		}
	}

	return buyer.Buyer{}, buyer.ErrNotFound
}

func (r buyers) Get(_ context.Context, id ids.UUID) (buyer.Buyer, error) {
	b, ok := r.d.buyers[id]
	if !ok {
		return buyer.Buyer{}, buyer.ErrNotFound
	}

	return b, nil
}

func (r buyers) Add(_ context.Context, b buyer.Buyer) error {
	for _, existing := range r.d.buyers {
		if existing.Phone == b.Phone {
			return buyer.ErrExists
		}
	}

	r.d.buyers[b.ID] = b

	return nil
}

func (r challenges) Get(_ context.Context, id ids.UUID) (*buyer.Challenge, error) {
	state, ok := r.d.challenges[id]
	if !ok {
		return nil, buyer.ErrChallengeNotFound
	}

	return buyer.RestoreChallenge(state), nil
}

func (r challenges) FindByProviderRef(_ context.Context, ref string) (*buyer.Challenge, error) {
	for _, state := range r.d.challenges {
		if state.ProviderRef == ref {
			return buyer.RestoreChallenge(state), nil
		}
	}

	return nil, buyer.ErrChallengeNotFound
}

func (r challenges) Add(_ context.Context, c *buyer.Challenge) error {
	r.d.challenges[c.ID()] = c.State()
	c.MarkSaved()

	return nil
}

func (r challenges) Update(_ context.Context, c *buyer.Challenge) error {
	stored, ok := r.d.challenges[c.ID()]
	status, version := c.Origin()

	if !ok || stored.Status != status || stored.Version != version {
		return buyer.ErrChallengeConflict
	}

	r.d.challenges[c.ID()] = c.State()
	c.MarkSaved()

	return nil
}

func (r carts) ByBuyer(_ context.Context, buyerID ids.UUID) (*cart.Cart, error) {
	state, ok := r.d.carts[buyerID]
	if !ok {
		return nil, cart.ErrNotFound
	}

	state.Lines = append([]cart.Line(nil), state.Lines...)

	return cart.Restore(state), nil
}

func (r carts) Save(_ context.Context, c *cart.Cart) error {
	state := c.State()
	stored, ok := r.d.carts[state.BuyerID]

	if (c.OriginVersion() == 0 && ok) || (c.OriginVersion() != 0 && (!ok || stored.Version != c.OriginVersion())) {
		return cart.ErrConflict
	}

	r.d.carts[state.BuyerID] = state
	c.MarkSaved()

	return nil
}

func (r carts) DeleteExpired(_ context.Context, before time.Time, limit int) (int64, error) {
	var removed int64

	for buyerID, state := range r.d.carts {
		if removed == int64(limit) {
			break
		}
		if state.ExpiresAt.Before(before) {
			delete(r.d.carts, buyerID)
			removed++
		}
	}

	return removed, nil
}

func copyCheckout(state checkout.State) checkout.State {
	groups := make([]checkout.Group, len(state.Groups))
	for i, group := range state.Groups {
		group.Lines = append([]checkout.Line(nil), group.Lines...)
		groups[i] = group
	}

	state.Groups = groups

	return state
}

func (r checkouts) Get(_ context.Context, id ids.UUID) (*checkout.Checkout, error) {
	state, ok := r.d.checkouts[id]
	if !ok {
		return nil, checkout.ErrNotFound
	}

	return checkout.Restore(copyCheckout(state)), nil
}

func (r checkouts) ByKey(_ context.Context, buyerID ids.UUID, key string) (*checkout.Checkout, error) {
	for _, state := range r.d.checkouts {
		if state.BuyerID == buyerID && state.IdempotencyKey == key {
			return checkout.Restore(copyCheckout(state)), nil
		}
	}

	return nil, checkout.ErrNotFound
}

func (r checkouts) Add(_ context.Context, c *checkout.Checkout) error {
	state := c.State()
	for _, existing := range r.d.checkouts {
		if existing.BuyerID == state.BuyerID && existing.IdempotencyKey == state.IdempotencyKey {
			return checkout.ErrDuplicateKey
		}
	}

	r.d.checkouts[state.ID] = copyCheckout(state)
	c.MarkSaved()

	return nil
}

func (r checkouts) Update(_ context.Context, c *checkout.Checkout) error {
	stored, ok := r.d.checkouts[c.ID()]
	status, version := c.Origin()

	if !ok || stored.Status != status || stored.Version != version {
		return checkout.ErrConflict
	}

	r.d.checkouts[c.ID()] = copyCheckout(c.State())
	c.MarkSaved()

	return nil
}

func (r pickupPoints) Upsert(_ context.Context, points []delivery.PickupPoint) error {
	for _, point := range points {
		r.d.points[pointKey{point.Carrier, point.Code}] = point
	}

	return nil
}

func (r pickupPoints) DeactivateStale(_ context.Context, carrier delivery.Carrier, since time.Time) (int64, error) {
	var closed int64

	for key, point := range r.d.points {
		if key.carrier == carrier && point.Active && point.RefreshedAt.Before(since) {
			point.Active = false
			r.d.points[key] = point
			closed++
		}
	}

	return closed, nil
}

func (r pickupPoints) Get(_ context.Context, carrier delivery.Carrier, code string) (delivery.PickupPoint, error) {
	point, ok := r.d.points[pointKey{carrier, code}]
	if !ok {
		return delivery.PickupPoint{}, delivery.ErrPickupPointNotFound
	}

	return point, nil
}

func (r pickupPoints) Page(
	_ context.Context,
	carrier delivery.Carrier,
	after string,
	limit int,
) ([]delivery.PickupPoint, error) {
	var page []delivery.PickupPoint

	for key, point := range r.d.points {
		if key.carrier == carrier && key.code > after {
			page = append(page, point)
		}
	}

	sort.Slice(page, func(i, j int) bool { return page[i].Code < page[j].Code })

	if len(page) > limit {
		page = page[:limit]
	}

	return page, nil
}
