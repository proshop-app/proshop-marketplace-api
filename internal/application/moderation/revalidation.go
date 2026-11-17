package moderation

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// revalidationTx — план страницы, а не второй кэш снимков. Срок жизни — одна транзакция;
// определение получено вызывающим под общей блокировкой версии.
type revalidationTx struct {
	ports.Tx
	data     map[ids.OfferID]ports.RevalidationModeration
	members  map[ids.TenantID]*participant.Participant
	products map[ids.OfferID]*catalog.Product
	current  *ports.RevalidationChange
}

type revalidationVerdicts struct {
	moderation.VerdictRepository
	page *revalidationTx
}

type revalidationCases struct {
	moderation.CaseRepository
	page *revalidationTx
}

type revalidationOffers struct {
	ports.OfferStates
	page *revalidationTx
}

// PlanRevalidation читает общие правила и участников один раз и использует ту же оценку, что приём события.
func (s *Service) PlanRevalidation(
	ctx context.Context,
	tx ports.Tx,
	products []*catalog.Product,
	definition taxonomy.Definition,
	checker taxonomy.Checker,
	now time.Time,
) ([]ports.RevalidationChange, error) {
	page, tenants, keys := newRevalidationTx(tx, products)
	rules, err := s.loadRevalidation(ctx, tx, page, tenants, keys)
	if err != nil {
		return nil, err
	}
	result := make([]ports.RevalidationChange, 0, len(products))
	for _, product := range products {
		state := product.State()
		key := ids.OfferID{Tenant: state.TenantID, Product: state.ProductID}
		change := ports.RevalidationChange{Product: product, Failure: page.data[key].Failure}
		page.current = &change
		if change.Failure == "" {
			member := page.members[state.TenantID]
			policy := checker.ModerationPolicy(state.Card.Publication.CategoryID)
			_, err := s.evaluatePrepared(ctx, page, product, member, nil, definition, rules, policy, now)
			if err != nil {
				if errs.CodeOf(err) != errs.Invalid {
					return nil, err
				}
				change.Failure = "evaluate: " + err.Error()
			}
		}
		result = append(result, change)
	}
	return result, nil
}

func (t *revalidationTx) AfterCommit(fn func()) {
	t.current.Observations = append(t.current.Observations, fn)
}

func (t *revalidationTx) Offers() ports.OfferStates {
	return revalidationOffers{OfferStates: t.Tx.Offers(), page: t}
}

func (t *revalidationTx) ModerationVerdicts() moderation.VerdictRepository {
	return revalidationVerdicts{VerdictRepository: t.Tx.ModerationVerdicts(), page: t}
}

func (t *revalidationTx) ModerationCases() moderation.CaseRepository {
	return revalidationCases{CaseRepository: t.Tx.ModerationCases(), page: t}
}

func (r revalidationVerdicts) Get(
	_ context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (*moderation.Verdict, error) {
	value := r.page.data[ids.OfferID{Tenant: tenant, Product: product}].Verdict
	if value == nil {
		return nil, moderation.ErrNotFound
	}
	return value, nil
}

func (r revalidationVerdicts) Save(_ context.Context, value *moderation.Verdict) error {
	r.page.current.Verdict = value
	return nil
}

func (r revalidationCases) Open(
	_ context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (*moderation.Case, error) {
	value := r.page.data[ids.OfferID{Tenant: tenant, Product: product}].Case
	if value == nil {
		return nil, moderation.ErrNotFound
	}
	return value, nil
}

func (r revalidationCases) Save(_ context.Context, value *moderation.Case) error {
	r.page.current.Case = value
	return nil
}

func (r revalidationOffers) Offer(
	_ context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (ports.OfferState, error) {
	key := ids.OfferID{Tenant: tenant, Product: product}
	data := r.page.data[key]
	return ports.OfferState{
		Product:     r.page.products[key],
		Signals:     data.Signals,
		Participant: r.page.members[tenant],
		Verdict:     data.Verdict,
	}, nil
}

func (s *Service) loadRevalidation(
	ctx context.Context,
	tx ports.Tx,
	page *revalidationTx,
	tenants []ids.TenantID,
	keys []ids.OfferID,
) (moderation.Rules, error) {
	rules := moderation.Rules{}
	var err error
	if len(tenants) > 0 {
		page.members, err = tx.Participants().Batch(ctx, tenants)
		if err != nil {
			return rules, err
		}
	}
	if len(page.members) > 0 {
		rules, err = tx.ModerationRules().Current(ctx)
		if err != nil {
			return rules, err
		}
		rules.RequireImage = s.config.RequireImage
	}
	page.data, err = tx.Revalidation().Moderation(ctx, keys)
	return rules, err
}

func newRevalidationTx(
	tx ports.Tx,
	products []*catalog.Product,
) (*revalidationTx, []ids.TenantID, []ids.OfferID) {
	tenants, keys := []ids.TenantID{}, []ids.OfferID{}
	page := &revalidationTx{
		Tx:       tx,
		products: map[ids.OfferID]*catalog.Product{},
	}
	seen := map[ids.TenantID]bool{}
	for _, product := range products {
		state := product.State()
		key := ids.OfferID{Tenant: state.TenantID, Product: state.ProductID}
		keys = append(keys, key)
		page.products[key] = product
		if state.Card.Publication.Published && !seen[state.TenantID] {
			tenants = append(tenants, state.TenantID)
			seen[state.TenantID] = true
		}
	}
	return page, tenants, keys
}
