package fake

import (
	"context"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
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
	UnknownFailures int
	FailedAt        *time.Time
	FailureReason   string
	ID              int64
	Kind            string
	TenantID        ids.TenantID
	DedupKey        string
	Payload         []byte
	Attempts        int
	NextAt          time.Time
	DoneAt          *time.Time
	LastError       string
	CreatedAt       time.Time
}

type data struct {
	databaseNow          func() time.Time
	revalidationFailures map[productKey]ports.RevalidationFailure
	features             *buyerFeaturesData
	storeCards           map[ids.TenantID]ports.StoreRecord
	promotion            *promotionData
	moderation           *moderationData
	decisions            map[string]ports.EventDecision
	taxonomy             taxonomy.Definition
	taxonomyVersions     map[int64]taxonomy.Definition
	taxonomyRelease      *taxonomy.ReleaseRecord
	revalidations        []taxonomy.Revalidation
	participants         map[ids.TenantID]participant.State
	tenants              map[ids.TenantID]participant.TenantState
	products             map[productKey]catalog.ProductState
	signals              map[productKey]catalog.SignalsState
	outbox               []OutboxRow
	nextTaskID           int64
	nextParkedID         int64
	nextCheckoutNumber   int64
	processed            map[string]time.Time
	parked               map[parkedKey]ports.ParkedEvent
	buyers               map[ids.UUID]buyer.Buyer
	challenges           map[ids.UUID]buyer.ChallengeState
	carts                map[ids.UUID]cart.State
	checkouts            map[ids.UUID]checkout.State
	checkoutReadFailures map[ids.UUID]checkout.ReadFailure
	deferredWork         map[ids.UUID]time.Time
	leases               map[string]lease
	reference            *referenceData
	synonyms             *synonymData
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
		databaseNow:          d.databaseNow,
		features:             d.features.clone(),
		storeCards:           maps.Clone(d.storeCards),
		promotion:            d.promotion.clone(),
		moderation:           d.moderation.clone(),
		taxonomy:             d.taxonomy,
		revalidations:        slices.Clone(d.revalidations),
		revalidationFailures: maps.Clone(d.revalidationFailures),
		taxonomyVersions:     maps.Clone(d.taxonomyVersions),
		taxonomyRelease:      cloneRelease(d.taxonomyRelease),
		decisions:            maps.Clone(d.decisions),
		participants:         maps.Clone(d.participants),
		tenants:              maps.Clone(d.tenants),
		products:             maps.Clone(d.products),
		signals:              maps.Clone(d.signals),
		outbox:               append([]OutboxRow(nil), d.outbox...),
		nextTaskID:           d.nextTaskID,
		nextParkedID:         d.nextParkedID,
		nextCheckoutNumber:   d.nextCheckoutNumber,
		processed:            maps.Clone(d.processed),
		parked:               maps.Clone(d.parked),
		buyers:               maps.Clone(d.buyers),
		challenges:           maps.Clone(d.challenges),
		carts:                maps.Clone(d.carts),
		checkouts:            maps.Clone(d.checkouts),
		checkoutReadFailures: maps.Clone(d.checkoutReadFailures),
		deferredWork:         maps.Clone(d.deferredWork),
		leases:               maps.Clone(d.leases),
		reference:            d.reference.clone(),
		synonyms:             d.synonyms.clone(),
	}
}

// NewStore — пустое хранилище.
func NewStore() *Store {
	return &Store{data: &data{
		databaseNow:          time.Now,
		features:             newBuyerFeaturesData(),
		taxonomyVersions:     map[int64]taxonomy.Definition{},
		revalidationFailures: map[productKey]ports.RevalidationFailure{},
		storeCards:           map[ids.TenantID]ports.StoreRecord{},
		promotion:            newPromotionData(),
		moderation:           newModerationData(),
		decisions:            map[string]ports.EventDecision{},
		participants:         map[ids.TenantID]participant.State{},
		tenants:              map[ids.TenantID]participant.TenantState{},
		products:             map[productKey]catalog.ProductState{},
		signals:              map[productKey]catalog.SignalsState{},
		processed:            map[string]time.Time{},
		parked:               map[parkedKey]ports.ParkedEvent{},
		buyers:               map[ids.UUID]buyer.Buyer{},
		challenges:           map[ids.UUID]buyer.ChallengeState{},
		carts:                map[ids.UUID]cart.State{},
		checkouts:            map[ids.UUID]checkout.State{},
		checkoutReadFailures: map[ids.UUID]checkout.ReadFailure{},
		deferredWork:         map[ids.UUID]time.Time{},
		leases:               map[string]lease{},
		reference:            newReferenceData(),
	}}
}

// SetDatabaseClock задаёт часы fake-БД для проверки аренды без реального ожидания.
func (s *Store) SetDatabaseClock(clock ports.Clock) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.databaseNow = clock.Now
}

// Do выполняет fn на копии данных и фиксирует её, только если fn вернула nil.
func (s *Store) Do(ctx context.Context, fn func(ctx context.Context, tx ports.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	working := s.data.clone()
	scope := &tx{data: working}
	if err := fn(ctx, scope); err != nil {
		return err
	}
	if s.FailCommit != nil {
		return s.FailCommit
	}

	s.data = working
	for _, observe := range scope.afterCommit {
		observe()
	}

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

// Claim повторяет обход взятия в PostgreSQL: полосы «вид × магазин» по кругу от курсора, не больше
// ports.ClaimLaneVisits посещений на вид; полоса готова, только если отдаёт задачу.
func (s *Store) Claim(_ context.Context, request ports.ClaimRequest) (ports.ClaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	walk := claimWalk{request: request, rows: s.data.outbox}
	kinds := walk.kinds()
	if len(kinds) == 0 {
		return ports.ClaimResult{Next: request.After}, nil
	}
	for _, kind := range kinds {
		walk.visit(kind)
	}
	tasks := walk.take()
	taken := make(map[int64]int, len(tasks))
	for n := range tasks {
		taken[tasks[n].ID] = n
	}
	for n := range s.data.outbox {
		if len(taken) == 0 {
			break
		}
		row := &s.data.outbox[n]
		index, claimed := taken[row.ID]
		if claimed {
			row.Attempts++
			row.NextAt = request.LeaseUntil
			tasks[index].Attempts = row.Attempts
			delete(taken, row.ID)
		}
	}
	return ports.ClaimResult{
		Tasks: tasks,
		Next:  walk.next(),
	}, nil
}

// Complete отмечает задачу выполненной.
func (s *Store) Complete(_ context.Context, id int64, attempts int, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.outbox {
		row := &s.data.outbox[i]
		if row.ID == id && row.Attempts == attempts && attempts > 0 && row.DoneAt == nil && row.FailedAt == nil {
			row.DoneAt = &at
			row.LastError = ""

			return nil
		}
	}

	return ports.ErrTaskClaimConflict
}

// Retry откладывает задачу.
func (s *Store) Retry(_ context.Context, id int64, attempts int, nextAt time.Time, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.outbox {
		row := &s.data.outbox[i]
		if row.ID == id && row.Attempts == attempts && attempts > 0 && row.DoneAt == nil && row.FailedAt == nil {
			row.NextAt = nextAt
			row.LastError = reason

			return nil
		}
	}

	return ports.ErrTaskClaimConflict
}

// Purge удаляет служебные записи старше границ.
func (s *Store) Purge(_ context.Context, request ports.PurgeRequest) (ports.PurgeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result ports.PurgeResult
	for key, failure := range s.data.revalidationFailures {
		state, exists := s.data.products[key]
		if !exists || !activeRevalidationFailure(state, failure) {
			delete(s.data.revalidationFailures, key)
			result.RevalidationFailures++
		}
	}

	for id, at := range s.data.processed {
		if at.Before(request.ProcessedBefore) {
			delete(s.data.processed, id)
			result.Processed++
		}
	}

	kept := s.data.outbox[:0]
	for _, row := range s.data.outbox {
		expired := row.FailedAt != nil && row.FailedAt.Before(request.PurchaseQuarantineBefore)
		if row.Kind == "search.purchase" && expired {
			result.PurchaseQuarantine++

			continue
		}

		kept = append(kept, row)
	}
	s.data.outbox = kept

	for key, event := range s.data.parked {
		if event.Control.Status == "resolved" && event.Control.UpdatedAt.Before(request.ParkedBefore) {
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

	for key, cached := range s.data.moderation.cachedBulk {
		if cached.ExpiresAt.Before(request.BulkBefore) {
			delete(s.data.moderation.cachedBulk, key)
			result.BulkApprovals++
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

// RemoveParticipant моделирует исчезнувшую запись проекции.
func (s *Store) RemoveParticipant(id ids.TenantID) {
	s.read(func(d *data) { delete(d.participants, id) })
}

// RemoveTenant моделирует исчезнувшую запись реестра.
func (s *Store) RemoveTenant(id ids.TenantID) {
	s.read(func(d *data) { delete(d.tenants, id) })
}

// RemoveProduct моделирует отсутствие проекции товара.
func (s *Store) RemoveProduct(tenant ids.TenantID, product ids.ProductID) {
	s.read(func(d *data) { delete(d.products, productKey{tenant, product}) })
}

// RemoveVerdict моделирует ещё не оценённое предложение.
func (s *Store) RemoveVerdict(tenant ids.TenantID, product ids.ProductID) {
	s.read(func(d *data) { delete(d.moderation.verdicts, productKey{tenant, product}) })
}

// FailCheckoutRead имитирует ошибку разбора сохранённой покупки или группы.
func (s *Store) FailCheckoutRead(id ids.UUID, tenant ids.TenantID, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reason := checkout.ReadGroup
	if tenant == "" {
		reason = checkout.ReadHeader
	}
	s.data.checkoutReadFailures[id] = checkout.ReadFailure{
		ID:       id,
		BuyerID:  s.data.checkouts[id].BuyerID,
		TenantID: tenant,
		Cause:    cause,
		Reason:   reason,
	}
}
