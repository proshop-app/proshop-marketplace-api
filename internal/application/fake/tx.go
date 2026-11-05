package fake

import (
	"context"
	"sort"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type tx struct {
	afterCommit []func()
	data        *data
}

type participants struct{ d *data }

type registry struct{ d *data }

type products struct{ d *data }

type signals struct{ d *data }

type outbox struct{ d *data }

type inbox struct{ d *data }

type parked struct{ d *data }

type offers struct{ d *data }

func (t *tx) Participants() participant.Repository { return participants{t.data} }

func (t *tx) Registry() participant.Registry { return registry{t.data} }

func (t *tx) Products() catalog.ProductRepository { return products{t.data} }

func (t *tx) Signals() catalog.SignalsRepository { return signals{t.data} }

func (t *tx) Offers() ports.OfferStates { return offers{t.data} }

func (t *tx) Outbox() ports.Outbox { return outbox{t.data} }

func (t *tx) Inbox() ports.Inbox { return inbox{t.data} }

func (t *tx) Parked() ports.ParkedEvents { return parked{t.data} }

func (t *tx) Buyers() buyer.Repository { return buyers{t.data} }

func (t *tx) Challenges() buyer.ChallengeRepository { return challenges{t.data} }

func (t *tx) Carts() cart.Repository { return carts{t.data} }

func (t *tx) Checkouts() checkout.Repository { return checkouts{t.data} }

func (t *tx) PickupPoints() delivery.PickupPointRepository {
	return pickupPoints{t.data}
}

func (r participants) Get(_ context.Context, id ids.TenantID) (*participant.Participant, error) {
	state, ok := r.d.participants[id]
	if !ok {
		return nil, participant.ErrNotFound
	}

	return participant.Restore(state), nil
}

func (r participants) List(_ context.Context, page participant.Page) ([]*participant.Participant, int, error) {
	states := make([]participant.State, 0, len(r.d.participants))
	for _, state := range r.d.participants {
		if page.Trust == "" || state.Trust == page.Trust {
			states = append(states, state)
		}
	}

	sort.Slice(states, func(i, j int) bool {
		if !states[i].CreatedAt.Equal(states[j].CreatedAt) {
			return states[i].CreatedAt.Before(states[j].CreatedAt)
		}

		return states[i].TenantID < states[j].TenantID
	})

	total := len(states)
	items := make([]*participant.Participant, 0)

	for i := page.Offset; i < total && len(items) < page.Limit; i++ {
		items = append(items, participant.Restore(states[i]))
	}

	return items, total, nil
}

func (r participants) Add(_ context.Context, p *participant.Participant) error {
	state := p.State()
	if _, ok := r.d.participants[state.TenantID]; ok {
		return participant.ErrAlreadyExists
	}

	for _, other := range r.d.participants {
		if other.Slug == state.Slug {
			return participant.ErrSlugTaken
		}
	}

	r.d.participants[state.TenantID] = state
	p.MarkSaved()

	return nil
}

func (r participants) Update(_ context.Context, p *participant.Participant) error {
	state := p.State()
	stored, ok := r.d.participants[state.TenantID]

	if !ok || stored.Status != p.Origin().Status || stored.Version != p.Origin().Version {
		return participant.ErrConflict
	}

	for id, other := range r.d.participants {
		if id != state.TenantID && other.Slug == state.Slug {
			return participant.ErrSlugTaken
		}
	}

	r.d.participants[state.TenantID] = state
	p.MarkSaved()

	return nil
}

func (r registry) Get(_ context.Context, id ids.TenantID) (*participant.Tenant, error) {
	state, ok := r.d.tenants[id]
	if !ok {
		return nil, participant.ErrTenantNotFound
	}

	return participant.RestoreTenant(state), nil
}

func (r registry) Save(_ context.Context, tenant *participant.Tenant) error {
	state := tenant.State()
	stored, ok := r.d.tenants[state.TenantID]

	if (tenant.OriginVersion() == 0 && ok) ||
		(tenant.OriginVersion() != 0 && (!ok || stored.Version != tenant.OriginVersion())) {
		return participant.ErrTenantConflict
	}

	r.d.tenants[state.TenantID] = state
	tenant.MarkSaved()

	return nil
}

func (r products) Get(_ context.Context, tenant ids.TenantID, product ids.ProductID) (*catalog.Product, error) {
	state, ok := r.d.products[productKey{tenant, product}]
	if !ok {
		return nil, catalog.ErrProductNotFound
	}

	return catalog.RestoreProduct(state), nil
}

func (r products) Save(_ context.Context, product *catalog.Product) error {
	state := product.State()
	key := productKey{state.TenantID, state.ProductID}

	if !versionMatches(
		r.d.products,
		key,
		product.OriginVersion(),
		func(s catalog.ProductState) int64 { return s.Version },
	) {
		return catalog.ErrConflict
	}

	r.d.products[key] = state
	product.MarkSaved()

	return nil
}

func (r signals) Get(_ context.Context, tenant ids.TenantID, product ids.ProductID) (*catalog.Signals, error) {
	state, ok := r.d.signals[productKey{tenant, product}]
	if !ok {
		return nil, catalog.ErrSignalsNotFound
	}

	return catalog.RestoreSignals(state), nil
}

func (r signals) Save(_ context.Context, s *catalog.Signals) error {
	state := s.State()
	key := productKey{state.TenantID, state.ProductID}

	if !versionMatches(r.d.signals, key, s.OriginVersion(), func(s catalog.SignalsState) int64 { return s.Version }) {
		return catalog.ErrConflict
	}

	r.d.signals[key] = state
	s.MarkSaved()

	return nil
}

func versionMatches[K comparable, V any](rows map[K]V, key K, origin int64, version func(V) int64) bool {
	stored, ok := rows[key]
	if origin == 0 {
		return !ok
	}

	return ok && version(stored) == origin
}

func (o outbox) Enqueue(_ context.Context, task ports.NewTask) error {
	if task.DedupKey != "" {
		for _, row := range o.d.outbox {
			if row.DedupKey == task.DedupKey && row.DoneAt == nil && row.Attempts == 0 {
				return nil
			}
		}
	}

	o.d.nextTaskID++
	o.d.outbox = append(o.d.outbox, OutboxRow{
		ID:        o.d.nextTaskID,
		Kind:      task.Kind,
		TenantID:  task.TenantID,
		DedupKey:  task.DedupKey,
		Payload:   task.Payload,
		NextAt:    task.At,
		CreatedAt: task.At,
	})

	return nil
}

func (i inbox) MarkProcessed(_ context.Context, eventID string, at time.Time) (bool, error) {
	if _, ok := i.d.processed[eventID]; ok {
		return false, nil
	}

	i.d.processed[eventID] = at

	return true, nil
}

func (p parked) Park(_ context.Context, event ports.ParkedEvent) error {
	key := parkedKey{event.Topic, event.Partition, event.Offset}
	if _, ok := p.d.parked[key]; !ok {
		event.Control.Status = "parked"
		event.Control.Version = 1
		p.d.nextParkedID++
		event.Control.ID = p.d.nextParkedID
		event.Control.UpdatedAt = event.ParkedAt
		p.d.parked[key] = event
	}

	return nil
}

func (o offers) state(key productKey) ports.OfferState {
	state := ports.OfferState{Product: catalog.RestoreProduct(o.d.products[key])}
	if v, ok := o.d.moderation.verdicts[key]; ok {
		state.Verdict = moderation.RestoreVerdict(v)
	}

	if signals, ok := o.d.signals[key]; ok {
		state.Signals = catalog.RestoreSignals(signals)
	}
	if p, ok := o.d.participants[key.tenant]; ok {
		state.Participant = participant.Restore(p)
	}
	if tenant, ok := o.d.tenants[key.tenant]; ok {
		state.Tenant = participant.RestoreTenant(tenant)
	}

	return state
}

func (o offers) keys(match func(productKey) bool) []productKey {
	keys := make([]productKey, 0)
	for key := range o.d.products {
		if match(key) {
			keys = append(keys, key)
		}
	}

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].tenant != keys[j].tenant {
			return keys[i].tenant < keys[j].tenant
		}

		return keys[i].product < keys[j].product
	})

	return keys
}

func (o offers) page(keys []productKey, limit int) []ports.OfferState {
	states := make([]ports.OfferState, 0, limit)
	for _, key := range keys {
		if len(states) == limit {
			break
		}

		states = append(states, o.state(key))
	}

	return states
}

func after(key productKey, position ports.OfferKey) bool {
	if key.tenant != position.TenantID {
		return key.tenant > position.TenantID
	}

	return key.product > position.ProductID
}

func (o offers) Offer(_ context.Context, tenant ids.TenantID, product ids.ProductID) (ports.OfferState, error) {
	key := productKey{tenant, product}
	if _, ok := o.d.products[key]; !ok {
		return ports.OfferState{}, catalog.ErrProductNotFound
	}

	return o.state(key), nil
}

func (o offers) TenantOffers(
	_ context.Context,
	tenant ids.TenantID,
	afterProduct ids.ProductID,
	limit int,
) ([]ports.OfferState, error) {
	keys := o.keys(func(key productKey) bool {
		return key.tenant == tenant && key.product > afterProduct
	})

	return o.page(keys, limit), nil
}

func (o offers) ParticipantOffers(_ context.Context, position ports.OfferKey, limit int) ([]ports.OfferState, error) {
	keys := o.keys(func(key productKey) bool {
		_, ok := o.d.participants[key.tenant]

		return ok && after(key, position)
	})

	return o.page(keys, limit), nil
}

func (o offers) ChangedSince(
	_ context.Context,
	since time.Time,
	position ports.OfferKey,
	limit int,
) ([]ports.OfferState, error) {
	keys := o.keys(func(key productKey) bool {
		p, ok := o.d.participants[key.tenant]
		if !ok || !after(key, position) {
			return false
		}

		changed := !o.d.products[key].UpdatedAt.Before(since) || !p.UpdatedAt.Before(since)
		if v, ok := o.d.moderation.verdicts[key]; ok && !v.UpdatedAt.Before(since) {
			changed = true
		}
		if s, ok := o.d.signals[key]; ok && !s.UpdatedAt.Before(since) {
			changed = true
		}
		if t, ok := o.d.tenants[key.tenant]; ok && !t.UpdatedAt.Before(since) {
			changed = true
		}

		return changed
	})

	return o.page(keys, limit), nil
}

func (t *tx) Taxonomy() taxonomy.Repository { return taxonomies{t.data} }

func (t *tx) AfterCommit(fn func()) { t.afterCommit = append(t.afterCommit, fn) }
