package fake

import (
	"context"
	"sort"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type buyers struct{ d *data }

type challenges struct{ d *data }

type carts struct{ d *data }

type checkouts struct{ d *data }

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

func copyCheckout(state checkout.State) checkout.State { return checkout.Restore(state).State() }

func (r checkouts) Get(_ context.Context, id ids.UUID) (*checkout.Checkout, error) {
	state, ok := r.d.checkouts[id]
	if !ok {
		return nil, checkout.ErrNotFound
	}

	return checkout.Restore(copyCheckout(state)), nil
}

// GetForUpdate — то же чтение: транзакции хранилища в памяти и так идут по одной.
func (r checkouts) GetForUpdate(ctx context.Context, id ids.UUID) (*checkout.Checkout, error) {
	return r.Get(ctx, id)
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

	r.d.nextCheckoutNumber++
	c.AssignNumber(r.d.nextCheckoutNumber)
	r.d.checkouts[state.ID] = copyCheckout(c.State())
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

func (r checkouts) UpdateMany(ctx context.Context, cs []*checkout.Checkout) error {
	for _, c := range cs {
		if err := r.Update(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

func (r checkouts) List(_ context.Context, f checkout.Filter) ([]*checkout.Checkout, error) {
	var found []*checkout.Checkout
	for _, state := range r.d.checkouts {
		if state.ID <= f.AfterID || (f.BuyerID != "" && state.BuyerID != f.BuyerID) {
			continue
		}
		if f.OrdersOnly && !state.CartConsumed &&
			(state.Status == checkout.StatusDraft ||
				state.Status == checkout.StatusConfirmed) {
			continue
		}
		if f.HasOrders && !checkout.Restore(state).HasOrders() {
			continue
		}
		if !f.StuckBefore.IsZero() {
			stuck := false
			for _, g := range state.Groups {
				if g.ReconciliationRequired ||
					(g.Status == checkout.GroupAwaitingPayment && !g.AwaitingSince.After(f.StuckBefore)) {
					stuck = true
				}
			}
			if !stuck {
				continue
			}
		}
		found = append(found, checkout.Restore(copyCheckout(state)))
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ID() < found[j].ID() })
	if len(found) > f.Limit {
		found = found[:f.Limit]
	}
	return found, nil
}

func (r checkouts) LockWorkDue(
	_ context.Context,
	now time.Time,
	after ids.UUID,
	limit int,
) ([]*checkout.Checkout, error) {
	var found []*checkout.Checkout
	for _, state := range r.d.checkouts {
		ordered := state.CartConsumed ||
			(state.Status != checkout.StatusDraft && state.Status != checkout.StatusConfirmed)
		if state.ID > after && ordered && checkoutWorkDue(state, now) {
			found = append(found, checkout.Restore(copyCheckout(state)))
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ID() < found[j].ID() })
	if len(found) > limit {
		found = found[:limit]
	}
	return found, nil
}

func (r checkouts) PickupHistory(_ context.Context, buyerID ids.UUID, limit int) ([]checkout.Selection, error) {
	var states []checkout.State
	for _, state := range r.d.checkouts {
		if state.BuyerID == buyerID && state.Delivery != nil && checkout.Restore(state).HasOrders() {
			states = append(states, state)
		}
	}
	sort.SliceStable(states, func(i, j int) bool { return states[i].CreatedAt.After(states[j].CreatedAt) })
	seen := map[string]bool{}
	var found []checkout.Selection
	for _, state := range states {
		key := string(state.Delivery.Carrier) + ":" + state.Delivery.PickupPointCode
		if seen[key] || len(found) == limit {
			continue
		}
		seen[key] = true
		found = append(found, *state.Delivery)
	}
	return found, nil
}

func (r checkouts) ReconcileDue(
	_ context.Context,
	after checkout.ReconcileCursor,
	now time.Time,
	limit int,
) ([]checkout.ReconcileEntry, error) {
	var found []checkout.ReconcileEntry
	for _, state := range r.d.checkouts {
		for _, g := range state.Groups {
			passed := g.TenantID < after.TenantID ||
				(g.TenantID == after.TenantID && state.ID <= after.CheckoutID)
			if passed || !g.ReconcileUntil().After(now) {
				continue
			}
			found = append(found, checkout.ReconcileEntry{
				CheckoutID:  state.ID,
				TenantID:    g.TenantID,
				OrderID:     g.OrderID,
				Status:      g.EffectiveStatus(),
				Revision:    g.OrderRevision,
				Unreachable: g.Status == checkout.GroupSellerUnreachable,
			})
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].TenantID != found[j].TenantID {
			return found[i].TenantID < found[j].TenantID
		}
		return found[i].CheckoutID < found[j].CheckoutID
	})
	if len(found) > limit {
		found = found[:limit]
	}
	return found, nil
}

// ExpireReconciliation в памяти не нужен: срок сверки считается из состояния группы.
func (r checkouts) ExpireReconciliation(context.Context, time.Time) error { return nil }

func checkoutWorkDue(state checkout.State, now time.Time) bool {
	for _, g := range state.Groups {
		if g.PollDue(now) || g.ReturnDue(now) {
			return true
		}
	}
	return false
}
