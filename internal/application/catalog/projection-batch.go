package catalog

import (
	"context"
	"slices"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// projectionBatch — транзакция пачки снимков разных товаров. Первое чтение товара, вердикта,
// открытого дела или участника читает их сразу для всей пачки одним запросом; таксономия и
// правила модерации читаются один раз; сохранения товаров и вердиктов и задачи outbox пишутся
// в конце одним оператором на таблицу. Чего снимку не нужно, то не читается вовсе.
// Прочитанная заранее запись товара, вердикта и дела отдаётся один раз: повторное чтение идёт
// в базу, как без пачки, и перед ним отложенные сохранения дописываются — чтение видит своё.
type projectionBatch struct {
	ports.Tx
	keys         []ids.OfferID
	tenants      []ids.TenantID
	writes       *deferredWrites
	products     *batchProducts
	verdicts     *batchVerdicts
	cases        *batchCases
	participants *batchParticipants
	taxonomy     *batchTaxonomy
	rules        *batchRules
	offers       *batchOffers
	sources      *batchSources
	outbox       *batchOutbox
}

// deferredWrites — сохранения пачки до одного оператора на таблицу. Агрегат в очереди один раз:
// он несёт последнее состояние, а исходная версия не меняется до записи.
type deferredWrites struct {
	products      catalog.ProductRepository
	verdicts      moderation.VerdictRepository
	queuedProduct []*catalog.Product
	queuedVerdict []*moderation.Verdict
}

// batchOffers дописывает отложенное перед чтением снимков предложения из базы.
type batchOffers struct {
	ports.OfferStates
	writes *deferredWrites
}

// batchSources дописывает отложенное перед чтением прежнего вердикта для повторного допуска.
type batchSources struct {
	ports.ProjectionValidation
	writes *deferredWrites
}

// prefetched — записи пачки: nil — записи нет; ключа нет — уже отдан или не из пачки, читать из базы.
type prefetched[T any] struct {
	keys   []ids.OfferID
	loaded map[ids.OfferID]*T
	read   func(context.Context, []ids.OfferID) (map[ids.OfferID]*T, error)
}

type batchProducts struct {
	catalog.ProductRepository
	pending prefetched[catalog.Product]
	writes  *deferredWrites
}

type batchVerdicts struct {
	moderation.VerdictRepository
	pending prefetched[moderation.Verdict]
	writes  *deferredWrites
}

type batchCases struct {
	moderation.CaseRepository
	pending prefetched[moderation.Case]
}

// batchParticipants отдаёт каждому снимку свою копию участника: приём участника не меняет.
type batchParticipants struct {
	participant.Repository
	tenants []ids.TenantID
	loaded  map[ids.TenantID]*participant.State
}

type batchTaxonomy struct {
	taxonomy.Repository
	read       bool
	definition taxonomy.Definition
	err        error
}

type batchRules struct {
	moderation.RulesRepository
	read  bool
	rules moderation.Rules
	err   error
}

// batchOutbox склеивает задачи пачки с одним ключом так же, как склеивает невзятые задачи база:
// остаётся первая, срок — ближайший.
type batchOutbox struct {
	tasks []ports.NewTask
	keys  map[string]int
}

func newProjectionBatch(tx ports.Tx, events []ProductEvent) *projectionBatch {
	keys, tenants := projectionKeys(events)
	return &projectionBatch{
		Tx:      tx,
		keys:    keys,
		tenants: tenants,
		writes:  &deferredWrites{},
		outbox:  &batchOutbox{keys: map[string]int{}},
	}
}

// Products — товары пачки.
func (b *projectionBatch) Products() catalog.ProductRepository {
	if b.products == nil {
		repository := b.Tx.Products()
		b.writes.products = repository
		b.products = &batchProducts{
			ProductRepository: repository,
			pending:           prefetched[catalog.Product]{keys: b.keys, read: repository.GetMany},
			writes:            b.writes,
		}
	}
	return b.products
}

// ModerationVerdicts — вердикты пачки.
func (b *projectionBatch) ModerationVerdicts() moderation.VerdictRepository {
	if b.verdicts == nil {
		repository := b.Tx.ModerationVerdicts()
		b.writes.verdicts = repository
		b.verdicts = &batchVerdicts{
			VerdictRepository: repository,
			pending:           prefetched[moderation.Verdict]{keys: b.keys, read: repository.GetMany},
			writes:            b.writes,
		}
	}
	return b.verdicts
}

// ModerationCases — открытые дела пачки.
func (b *projectionBatch) ModerationCases() moderation.CaseRepository {
	if b.cases == nil {
		repository := b.Tx.ModerationCases()
		b.cases = &batchCases{
			CaseRepository: repository,
			pending:        prefetched[moderation.Case]{keys: b.keys, read: repository.OpenMany},
		}
	}
	return b.cases
}

// Participants — участники магазинов пачки.
func (b *projectionBatch) Participants() participant.Repository {
	if b.participants == nil {
		b.participants = &batchParticipants{Repository: b.Tx.Participants(), tenants: b.tenants}
	}
	return b.participants
}

// Taxonomy — действующая таксономия, одна на пачку.
func (b *projectionBatch) Taxonomy() taxonomy.Repository {
	if b.taxonomy == nil {
		b.taxonomy = &batchTaxonomy{Repository: b.Tx.Taxonomy()}
	}
	return b.taxonomy
}

// ModerationRules — правила модерации, одни на пачку.
func (b *projectionBatch) ModerationRules() moderation.RulesRepository {
	if b.rules == nil {
		b.rules = &batchRules{RulesRepository: b.Tx.ModerationRules()}
	}
	return b.rules
}

// Offers — снимки предложений из базы после записи отложенного.
func (b *projectionBatch) Offers() ports.OfferStates {
	if b.offers == nil {
		b.offers = &batchOffers{OfferStates: b.Tx.Offers(), writes: b.writes}
	}
	return b.offers
}

// ProjectionValidation — источники повторного допуска после записи отложенного.
func (b *projectionBatch) ProjectionValidation() ports.ProjectionValidation {
	if b.sources == nil {
		b.sources = &batchSources{ProjectionValidation: b.Tx.ProjectionValidation(), writes: b.writes}
	}
	return b.sources
}

// Outbox копит задачи пачки до flush.
func (b *projectionBatch) Outbox() ports.Outbox { return b.outbox }

// flush пишет отложенные товары и вердикты и задачи outbox пачки — по оператору на таблицу.
func (b *projectionBatch) flush(ctx context.Context) error {
	if err := b.writes.flush(ctx); err != nil {
		return err
	}
	if len(b.outbox.tasks) == 0 {
		return nil
	}
	return b.Tx.Outbox().EnqueueBatch(ctx, b.outbox.tasks)
}

func (w *deferredWrites) flush(ctx context.Context) error {
	if len(w.queuedProduct) > 0 {
		if err := w.products.SaveMany(ctx, w.queuedProduct); err != nil {
			return err
		}
		w.queuedProduct = nil
	}
	if len(w.queuedVerdict) > 0 {
		if err := w.verdicts.SaveMany(ctx, w.queuedVerdict); err != nil {
			return err
		}
		w.queuedVerdict = nil
	}
	return nil
}

// take отдаёт запись пачки один раз; false — ключ не из пачки или уже отдан.
func (p *prefetched[T]) take(ctx context.Context, key ids.OfferID) (*T, bool, error) {
	if p.loaded == nil {
		found, err := p.read(ctx, p.keys)
		if err != nil {
			return nil, false, err
		}
		for _, wanted := range p.keys {
			if _, ok := found[wanted]; !ok {
				found[wanted] = nil
			}
		}
		p.loaded = found
	}
	value, ok := p.loaded[key]
	delete(p.loaded, key)
	return value, ok, nil
}

func (r *batchProducts) Get(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (*catalog.Product, error) {
	found, ok, err := r.pending.take(ctx, ids.OfferID{Tenant: tenant, Product: product})
	switch {
	case err != nil:
		return nil, err
	case !ok:
		if err := r.writes.flush(ctx); err != nil {
			return nil, err
		}
		return r.ProductRepository.Get(ctx, tenant, product)
	case found == nil:
		return nil, catalog.ErrProductNotFound
	}
	return found, nil
}

// Save откладывает запись товара до конца пачки.
func (r *batchProducts) Save(_ context.Context, product *catalog.Product) error {
	if !slices.Contains(r.writes.queuedProduct, product) {
		r.writes.queuedProduct = append(r.writes.queuedProduct, product)
	}
	return nil
}

func (r *batchVerdicts) Get(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (*moderation.Verdict, error) {
	found, ok, err := r.pending.take(ctx, ids.OfferID{Tenant: tenant, Product: product})
	switch {
	case err != nil:
		return nil, err
	case !ok:
		if err := r.writes.flush(ctx); err != nil {
			return nil, err
		}
		return r.VerdictRepository.Get(ctx, tenant, product)
	case found == nil:
		return nil, moderation.ErrNotFound
	}
	return found, nil
}

// Save откладывает запись вердикта до конца пачки.
func (r *batchVerdicts) Save(_ context.Context, verdict *moderation.Verdict) error {
	if !slices.Contains(r.writes.queuedVerdict, verdict) {
		r.writes.queuedVerdict = append(r.writes.queuedVerdict, verdict)
	}
	return nil
}

func (o *batchOffers) WalkStoreSummaryOffers(
	ctx context.Context,
	tenant ids.TenantID,
	walk func([]ports.OfferState) error,
) error {
	if err := o.writes.flush(ctx); err != nil {
		return err
	}
	return o.OfferStates.WalkStoreSummaryOffers(ctx, tenant, walk)
}

func (o *batchOffers) StatusBatch(ctx context.Context, keys []ids.OfferID) ([]ports.OfferStatus, error) {
	if err := o.writes.flush(ctx); err != nil {
		return nil, err
	}
	return o.OfferStates.StatusBatch(ctx, keys)
}

func (o *batchOffers) Batch(ctx context.Context, keys []ids.OfferID) ([]ports.OfferState, error) {
	if err := o.writes.flush(ctx); err != nil {
		return nil, err
	}
	return o.OfferStates.Batch(ctx, keys)
}

func (o *batchOffers) Offer(ctx context.Context, tenant ids.TenantID, product ids.ProductID) (ports.OfferState, error) {
	if err := o.writes.flush(ctx); err != nil {
		return ports.OfferState{}, err
	}
	return o.OfferStates.Offer(ctx, tenant, product)
}

func (o *batchOffers) TenantOffers(
	ctx context.Context,
	tenant ids.TenantID,
	after ids.ProductID,
	limit int,
) ([]ports.OfferState, error) {
	if err := o.writes.flush(ctx); err != nil {
		return nil, err
	}
	return o.OfferStates.TenantOffers(ctx, tenant, after, limit)
}

func (o *batchOffers) ParticipantOffers(
	ctx context.Context,
	after ports.OfferKey,
	limit int,
) ([]ports.OfferState, error) {
	if err := o.writes.flush(ctx); err != nil {
		return nil, err
	}
	return o.OfferStates.ParticipantOffers(ctx, after, limit)
}

func (o *batchOffers) ChangedSince(
	ctx context.Context,
	since time.Time,
	after ports.OfferKey,
	limit int,
) ([]ports.OfferState, error) {
	if err := o.writes.flush(ctx); err != nil {
		return nil, err
	}
	return o.OfferStates.ChangedSince(ctx, since, after, limit)
}

func (v *batchSources) Current(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (ports.ValidationSources, error) {
	if err := v.writes.flush(ctx); err != nil {
		return ports.ValidationSources{}, err
	}
	return v.ProjectionValidation.Current(ctx, tenant, product)
}

func (r *batchCases) Open(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (*moderation.Case, error) {
	found, ok, err := r.pending.take(ctx, ids.OfferID{Tenant: tenant, Product: product})
	switch {
	case err != nil:
		return nil, err
	case !ok:
		return r.CaseRepository.Open(ctx, tenant, product)
	case found == nil:
		return nil, moderation.ErrNotFound
	}
	return found, nil
}

func (r *batchParticipants) Get(ctx context.Context, tenant ids.TenantID) (*participant.Participant, error) {
	if r.loaded == nil {
		if err := r.load(ctx); err != nil {
			return nil, err
		}
	}
	state, ok := r.loaded[tenant]
	if !ok {
		return r.Repository.Get(ctx, tenant)
	}
	if state == nil {
		return nil, participant.ErrNotFound
	}
	return participant.Restore(*state), nil
}

// Update забывает участника пачки: следующее чтение увидит сохранённое.
func (r *batchParticipants) Update(ctx context.Context, member *participant.Participant) error {
	delete(r.loaded, member.State().TenantID)
	return r.Repository.Update(ctx, member)
}

func (r *batchParticipants) load(ctx context.Context) error {
	members, err := r.Batch(ctx, r.tenants)
	if err != nil {
		return err
	}
	r.loaded = make(map[ids.TenantID]*participant.State, len(r.tenants))
	for _, tenant := range r.tenants {
		r.loaded[tenant] = nil
		if member, ok := members[tenant]; ok {
			state := member.State()
			r.loaded[tenant] = &state
		}
	}
	return nil
}

func (r *batchTaxonomy) Current(ctx context.Context) (taxonomy.Definition, error) {
	if !r.read {
		r.definition, r.err = r.Repository.Current(ctx)
		r.read = true
	}
	return r.definition, r.err
}

func (r *batchRules) Current(ctx context.Context) (moderation.Rules, error) {
	if !r.read {
		r.rules, r.err = r.RulesRepository.Current(ctx)
		r.read = true
	}
	return r.rules, r.err
}

func (o *batchOutbox) Enqueue(_ context.Context, task ports.NewTask) error {
	o.add(task)
	return nil
}

func (o *batchOutbox) EnqueueBatch(_ context.Context, tasks []ports.NewTask) error {
	for _, task := range tasks {
		o.add(task)
	}
	return nil
}

func (o *batchOutbox) add(task ports.NewTask) {
	if task.DedupKey == "" {
		o.tasks = append(o.tasks, task)
		return
	}
	position, ok := o.keys[task.DedupKey]
	if !ok {
		o.keys[task.DedupKey] = len(o.tasks)
		o.tasks = append(o.tasks, task)
		return
	}
	first := &o.tasks[position]
	if due := taskDue(task); due.Before(taskDue(*first)) {
		first.AvailableAt = due
	}
}

func taskDue(task ports.NewTask) time.Time {
	if task.AvailableAt.IsZero() {
		return task.At
	}
	return task.AvailableAt
}

func projectionKeys(events []ProductEvent) ([]ids.OfferID, []ids.TenantID) {
	keys := make([]ids.OfferID, 0, len(events))
	tenants := make([]ids.TenantID, 0, len(events))
	seen := make(map[ids.TenantID]bool, len(events))
	for _, event := range events {
		snapshot := event.Snapshot
		keys = append(keys, ids.OfferID{Tenant: snapshot.TenantID, Product: snapshot.ProductID})
		if !seen[snapshot.TenantID] {
			seen[snapshot.TenantID] = true
			tenants = append(tenants, snapshot.TenantID)
		}
	}
	return keys, tenants
}
