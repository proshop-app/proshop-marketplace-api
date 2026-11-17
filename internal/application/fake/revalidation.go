package fake

import (
	"context"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type revalidations struct{ d *data }

func (r revalidations) Page(
	ctx context.Context,
	job taxonomy.Revalidation,
	limit int,
) ([]ports.RevalidationItem, error) {
	keys := offers(r).keys(func(key productKey) bool {
		card := r.d.products[key].Card.Marketplace
		return card != nil && card.CategoryID == job.Category &&
			(string(key.tenant) > job.TenantCursor ||
				string(key.tenant) == job.TenantCursor && string(key.product) > job.ProductCursor)
	})
	result := []ports.RevalidationItem{}
	for _, key := range keys[:min(limit, len(keys))] {
		state := r.d.products[key]
		result = append(result, ports.RevalidationItem{
			Key:      ids.OfferID{Tenant: state.TenantID, Product: state.ProductID},
			Version:  state.Version,
			Revision: state.Revision,
			Product:  catalog.RestoreProduct(state),
		})
	}
	return result, nil
}

func (r revalidations) Moderation(
	_ context.Context,
	keys []ids.OfferID,
) (map[ids.OfferID]ports.RevalidationModeration, error) {
	result := map[ids.OfferID]ports.RevalidationModeration{}
	for _, key := range keys {
		stored := productKey{tenant: key.Tenant, product: key.Product}
		value := ports.RevalidationModeration{}
		if state, ok := r.d.moderation.verdicts[stored]; ok {
			value.Verdict = moderation.RestoreVerdict(state)
		}
		for _, state := range r.d.moderation.cases {
			if state.TenantID == key.Tenant && state.ProductID == key.Product && state.Status == moderation.Open {
				value.Case = moderation.RestoreCase(state)
			}
		}
		if state, ok := r.d.signals[stored]; ok {
			value.Signals = catalog.RestoreSignals(state)
		}
		result[key] = value
	}
	return result, nil
}

func (r revalidations) Save(
	ctx context.Context,
	changes []ports.RevalidationChange,
) ([]ports.RevalidationFailure, error) {
	for _, change := range changes {
		if err := products(r).Save(ctx, change.Product); err != nil {
			return nil, err
		}
		if change.Verdict != nil {
			if err := (moderationVerdicts{r.d.moderation}).Save(ctx, change.Verdict); err != nil {
				return nil, err
			}
		}
		if change.Case != nil {
			if err := (moderationCases{r.d.moderation}).Save(ctx, change.Case); err != nil {
				return nil, err
			}
		}
		for _, task := range change.Tasks {
			if err := outbox(r).Enqueue(ctx, task); err != nil {
				return nil, err
			}
		}
	}
	return nil, nil
}

func (r revalidations) RecordFailures(_ context.Context, failures []ports.RevalidationFailure) error {
	for _, failure := range failures {
		runes := []rune(failure.Diagnostic)
		failure.Diagnostic = string(runes[:min(ports.RevalidationDiagnosticLimit, len(runes))])
		key := productKey{tenant: failure.TenantID, product: failure.ProductID}
		r.d.revalidationFailures[key] = failure
	}
	return nil
}

func (r revalidations) Failures(
	_ context.Context,
	cursor ports.OfferKey,
	limit int,
) ([]ports.RevalidationFailure, error) {
	result := []ports.RevalidationFailure{}
	for key, failure := range r.d.revalidationFailures {
		state, exists := r.d.products[key]
		if exists && activeRevalidationFailure(state, failure) && after(key, cursor) {
			result = append(result, failure)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].TenantID < result[j].TenantID ||
			result[i].TenantID == result[j].TenantID && result[i].ProductID < result[j].ProductID
	})
	return result[:min(limit, len(result))], nil
}

func activeRevalidationFailure(state catalog.ProductState, failure ports.RevalidationFailure) bool {
	verified := state.Version != failure.ProductVersion && state.Card.Publication.Version >= failure.TaxonomyVersion
	return state.Revision == failure.SourceRevision && !verified
}
