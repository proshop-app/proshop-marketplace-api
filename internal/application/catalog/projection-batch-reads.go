package catalog

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// prefetched — записи пачки, прочитанные одним запросом при первом обращении. nil — записи нет;
// ключа нет — уже отдан или не из пачки, читать из базы. Запись отдаётся один раз.
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

// batchSources — источники повторного допуска пачки: головы таксономии и правил, участник и
// прежний вердикт.
type batchSources struct {
	ports.ProjectionValidation
	pending prefetched[ports.ValidationSources]
	writes  *deferredWrites
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

// sourcesReader приводит источники допуска к общему виду прочитанного заранее.
func sourcesReader(
	repository ports.ProjectionValidation,
) func(context.Context, []ids.OfferID) (map[ids.OfferID]*ports.ValidationSources, error) {
	return func(ctx context.Context, keys []ids.OfferID) (map[ids.OfferID]*ports.ValidationSources, error) {
		found, err := repository.CurrentMany(ctx, keys)
		if err != nil {
			return nil, err
		}
		sources := make(map[ids.OfferID]*ports.ValidationSources, len(found))
		for key, value := range found {
			sources[key] = &value
		}
		return sources, nil
	}
}

func (r *batchProducts) Get(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (*catalog.Product, error) {
	key := ids.OfferID{Tenant: tenant, Product: product}
	found, ok, err := r.pending.take(ctx, key)
	switch {
	case err != nil:
		return nil, err
	case !ok:
		if err := r.writes.flushKey(ctx, key); err != nil {
			return nil, err
		}
		return r.ProductRepository.Get(ctx, tenant, product)
	case found == nil:
		return nil, catalog.ErrProductNotFound
	}
	return found, nil
}

func (r *batchVerdicts) Get(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (*moderation.Verdict, error) {
	key := ids.OfferID{Tenant: tenant, Product: product}
	found, ok, err := r.pending.take(ctx, key)
	switch {
	case err != nil:
		return nil, err
	case !ok:
		if err := r.writes.flushKey(ctx, key); err != nil {
			return nil, err
		}
		return r.VerdictRepository.Get(ctx, tenant, product)
	case found == nil:
		return nil, moderation.ErrNotFound
	}
	return found, nil
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

func (v *batchSources) Current(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (ports.ValidationSources, error) {
	key := ids.OfferID{Tenant: tenant, Product: product}
	found, ok, err := v.pending.take(ctx, key)
	switch {
	case err != nil:
		return ports.ValidationSources{}, err
	case ok && found != nil:
		return *found, nil
	}
	// Повторное чтение своего ключа видит отложенный вердикт: дописываем только его.
	if err := v.writes.flushKey(ctx, key); err != nil {
		return ports.ValidationSources{}, err
	}
	return v.ProjectionValidation.Current(ctx, tenant, product)
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
