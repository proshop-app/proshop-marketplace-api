package fake

import (
	"context"
	"maps"
	"sort"
	"sync"
	"time"

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

type productKey struct {
	tenant  ids.TenantID
	product ids.ProductID
}

type parkedKey struct {
	topic     string
	partition int
	offset    int64
}

// OutboxRow — задача outbox в памяти.
type OutboxRow struct {
	ID        int64
	Kind      string
	TenantID  ids.TenantID
	DedupKey  string
	Payload   []byte
	Attempts  int
	NextAt    time.Time
	DoneAt    *time.Time
	LastError string
	CreatedAt time.Time
}

type data struct {
	decisions       map[string]ports.EventDecision
	taxonomy        taxonomy.Definition
	taxonomyRelease *taxonomy.ReleaseRecord
	participants    map[ids.TenantID]participant.State
	tenants         map[ids.TenantID]participant.TenantState
	products        map[productKey]catalog.ProductState
	signals         map[productKey]catalog.SignalsState
	outbox          []OutboxRow
	nextTaskID      int64
	nextParkedID    int64
	processed       map[string]time.Time
	parked          map[parkedKey]ports.ParkedEvent
	buyers          map[ids.UUID]buyer.Buyer
	challenges      map[ids.UUID]buyer.ChallengeState
	carts           map[ids.UUID]cart.State
	checkouts       map[ids.UUID]checkout.State
	points          map[pointKey]delivery.PickupPoint
	leases          map[string]lease
	reference       *referenceData
}

type pointKey struct {
	carrier delivery.Carrier
	code    string
}

type lease struct {
	holder string
	until  time.Time
}

// Store — хранилище в памяти: UnitOfWork, очередь задач и чистка по сроку.
type Store struct {
	mu   sync.Mutex
	data *data
	// FailCommit, если задан, возвращается вместо фиксации транзакции.
	FailCommit error
}

func (d *data) clone() *data {
	return &data{
		taxonomy:        d.taxonomy,
		taxonomyRelease: cloneRelease(d.taxonomyRelease),
		decisions:       maps.Clone(d.decisions),
		participants:    maps.Clone(d.participants),
		tenants:         maps.Clone(d.tenants),
		products:        maps.Clone(d.products),
		signals:         maps.Clone(d.signals),
		outbox:          append([]OutboxRow(nil), d.outbox...),
		nextTaskID:      d.nextTaskID,
		nextParkedID:    d.nextParkedID,
		processed:       maps.Clone(d.processed),
		parked:          maps.Clone(d.parked),
		buyers:          maps.Clone(d.buyers),
		challenges:      maps.Clone(d.challenges),
		carts:           maps.Clone(d.carts),
		checkouts:       maps.Clone(d.checkouts),
		points:          maps.Clone(d.points),
		leases:          maps.Clone(d.leases),
		reference:       d.reference.clone(),
	}
}

// NewStore — пустое хранилище.
func NewStore() *Store {
	return &Store{data: &data{
		decisions:    map[string]ports.EventDecision{},
		participants: map[ids.TenantID]participant.State{},
		tenants:      map[ids.TenantID]participant.TenantState{},
		products:     map[productKey]catalog.ProductState{},
		signals:      map[productKey]catalog.SignalsState{},
		processed:    map[string]time.Time{},
		parked:       map[parkedKey]ports.ParkedEvent{},
		buyers:       map[ids.UUID]buyer.Buyer{},
		challenges:   map[ids.UUID]buyer.ChallengeState{},
		carts:        map[ids.UUID]cart.State{},
		checkouts:    map[ids.UUID]checkout.State{},
		points:       map[pointKey]delivery.PickupPoint{},
		leases:       map[string]lease{},
		reference:    newReferenceData(),
	}}
}

// Do выполняет fn на копии данных и фиксирует её, только если fn вернула nil.
func (s *Store) Do(ctx context.Context, fn func(ctx context.Context, tx ports.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	working := s.data.clone()
	if err := fn(ctx, &tx{data: working}); err != nil {
		return err
	}
	if s.FailCommit != nil {
		return s.FailCommit
	}

	s.data = working

	return nil
}

func (s *Store) read(fn func(d *data)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fn(s.data)
}

// SeedTenant кладёт запись реестра.
func (s *Store) SeedTenant(state participant.TenantState) {
	s.read(func(d *data) { d.tenants[state.TenantID] = state })
}

// SeedParticipant кладёт участника.
func (s *Store) SeedParticipant(state participant.State) {
	s.read(func(d *data) { d.participants[state.TenantID] = state })
}

// SeedProduct кладёт товар проекции.
func (s *Store) SeedProduct(state catalog.ProductState) {
	s.read(func(d *data) { d.products[productKey{state.TenantID, state.ProductID}] = state })
}

// Participant возвращает сохранённого участника.
func (s *Store) Participant(id ids.TenantID) (participant.State, bool) {
	var (
		state participant.State
		ok    bool
	)

	s.read(func(d *data) { state, ok = d.participants[id] })

	return state, ok
}

// Tenant — запись реестра, если есть.
func (s *Store) Tenant(id ids.TenantID) (participant.TenantState, bool) {
	var (
		state participant.TenantState
		ok    bool
	)

	s.read(func(d *data) { state, ok = d.tenants[id] })

	return state, ok
}

// Product — товар проекции, если есть.
func (s *Store) Product(tenant ids.TenantID, product ids.ProductID) (catalog.ProductState, bool) {
	var (
		state catalog.ProductState
		ok    bool
	)

	s.read(func(d *data) { state, ok = d.products[productKey{tenant, product}] })

	return state, ok
}

// Signals — сигналы товара, если есть.
func (s *Store) Signals(tenant ids.TenantID, product ids.ProductID) (catalog.SignalsState, bool) {
	var (
		state catalog.SignalsState
		ok    bool
	)

	s.read(func(d *data) { state, ok = d.signals[productKey{tenant, product}] })

	return state, ok
}

// OutboxRows возвращает копию очереди для проверки сценариев.
func (s *Store) OutboxRows() []OutboxRow {
	var rows []OutboxRow

	s.read(func(d *data) { rows = append(rows, d.outbox...) })

	return rows
}

// Processed — событие отмечено обработанным.
func (s *Store) Processed(eventID string) bool {
	var ok bool

	s.read(func(d *data) { _, ok = d.processed[eventID] })

	return ok
}

// ParkedEvents — отравленные сообщения.
func (s *Store) ParkedEvents() []ports.ParkedEvent {
	var events []ports.ParkedEvent

	s.read(func(d *data) {
		for _, event := range d.parked {
			events = append(events, event)
		}
	})

	sort.Slice(events, func(i, j int) bool { return events[i].Offset < events[j].Offset })

	return events
}

// Claim берёт созревшие задачи с арендой, не больше PerTenant на магазин.
func (s *Store) Claim(_ context.Context, request ports.ClaimRequest) ([]ports.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	due := make([]int, 0)
	for i, row := range s.data.outbox {
		if row.DoneAt == nil && !row.NextAt.After(request.Now) {
			due = append(due, i)
		}
	}

	sort.Slice(due, func(a, b int) bool {
		left, right := s.data.outbox[due[a]], s.data.outbox[due[b]]
		if !left.NextAt.Equal(right.NextAt) {
			return left.NextAt.Before(right.NextAt)
		}

		return left.ID < right.ID
	})

	perTenant := map[ids.TenantID]int{}
	tasks := make([]ports.Task, 0, request.Limit)

	for _, i := range due {
		row := &s.data.outbox[i]
		if len(tasks) == request.Limit || perTenant[row.TenantID] == request.PerTenant {
			continue
		}

		perTenant[row.TenantID]++
		row.Attempts++
		row.NextAt = request.LeaseUntil
		tasks = append(tasks, ports.Task{
			ID:        row.ID,
			Kind:      row.Kind,
			TenantID:  row.TenantID,
			Payload:   row.Payload,
			Attempts:  row.Attempts,
			CreatedAt: row.CreatedAt,
		})
	}

	return tasks, nil
}

// Complete отмечает задачу выполненной.
func (s *Store) Complete(_ context.Context, id int64, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.outbox {
		if s.data.outbox[i].ID == id {
			done := at
			s.data.outbox[i].DoneAt = &done
		}
	}

	return nil
}

// Retry откладывает задачу.
func (s *Store) Retry(_ context.Context, id int64, nextAt time.Time, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.outbox {
		if s.data.outbox[i].ID == id {
			s.data.outbox[i].NextAt = nextAt
			s.data.outbox[i].LastError = reason
		}
	}

	return nil
}

// Purge удаляет служебные записи старше границ.
func (s *Store) Purge(_ context.Context, request ports.PurgeRequest) (ports.PurgeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result ports.PurgeResult

	for id, at := range s.data.processed {
		if at.Before(request.ProcessedBefore) {
			delete(s.data.processed, id)
			result.Processed++
		}
	}

	kept := s.data.outbox[:0]
	for _, row := range s.data.outbox {
		if row.DoneAt != nil && row.DoneAt.Before(request.OutboxDoneBefore) {
			result.OutboxDone++

			continue
		}

		kept = append(kept, row)
	}
	s.data.outbox = kept

	for key, event := range s.data.parked {
		if event.ParkedAt.Before(request.ParkedBefore) && event.Control.Status != "queued" {
			delete(s.data.parked, key)
			result.Parked++
		}
	}

	for id, challenge := range s.data.challenges {
		if challenge.ExpiresAt.Before(request.ChallengesBefore) {
			delete(s.data.challenges, id)
			result.Challenges++
		}
	}

	for buyerID, state := range s.data.carts {
		if state.ExpiresAt.Before(request.CartsBefore) {
			delete(s.data.carts, buyerID)
			result.Carts++
		}
	}

	return result, nil
}

// Acquire берёт аренду, если она свободна, истекла или уже у holder.
func (s *Store) Acquire(_ context.Context, name, holder string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if current, ok := s.data.leases[name]; ok && current.holder != holder && current.until.After(now) {
		return false, nil
	}

	s.data.leases[name] = lease{holder: holder, until: now.Add(ttl)}

	return true, nil
}

// Release отпускает аренду holder.
func (s *Store) Release(_ context.Context, name, holder string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if current, ok := s.data.leases[name]; ok && current.holder == holder {
		delete(s.data.leases, name)
	}

	return nil
}

// SeedPickupPoint кладёт пункт выдачи.
func (s *Store) SeedPickupPoint(point delivery.PickupPoint) {
	s.read(func(d *data) { d.points[pointKey{point.Carrier, point.Code}] = point })
}

// PickupPoint — пункт выдачи, если есть.
func (s *Store) PickupPoint(carrier delivery.Carrier, code string) (delivery.PickupPoint, bool) {
	var (
		point delivery.PickupPoint
		ok    bool
	)

	s.read(func(d *data) { point, ok = d.points[pointKey{carrier, code}] })

	return point, ok
}

// SeedCart кладёт корзину.
func (s *Store) SeedCart(state cart.State) {
	s.read(func(d *data) { d.carts[state.BuyerID] = state })
}

// Cart — корзина покупателя, если есть.
func (s *Store) Cart(buyerID ids.UUID) (cart.State, bool) {
	var (
		state cart.State
		ok    bool
	)

	s.read(func(d *data) { state, ok = d.carts[buyerID] })

	return state, ok
}

// Challenge — проверка входа, если есть.
func (s *Store) Challenge(id ids.UUID) (buyer.ChallengeState, bool) {
	var (
		state buyer.ChallengeState
		ok    bool
	)

	s.read(func(d *data) { state, ok = d.challenges[id] })

	return state, ok
}

// Buyers — все покупатели.
func (s *Store) Buyers() []buyer.Buyer {
	var list []buyer.Buyer

	s.read(func(d *data) {
		for _, b := range d.buyers {
			list = append(list, b)
		}
	})

	return list
}

// Checkout — покупка, если есть.
func (s *Store) Checkout(id ids.UUID) (checkout.State, bool) {
	var (
		state checkout.State
		ok    bool
	)

	s.read(func(d *data) { state, ok = d.checkouts[id] })

	return state, ok
}
