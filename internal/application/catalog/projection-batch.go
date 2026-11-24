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

// projectionBatch — транзакция пачки снимков разных товаров. Первое чтение товара, вердикта,
// открытого дела, источников допуска или участника читает их сразу для всей пачки одним
// запросом (projection-batch-reads.go); таксономия и правила читаются один раз; сохранения
// товаров и вердиктов (projection-batch-writes.go) и задачи outbox (projection-batch-outbox.go)
// пишутся в конце одним оператором на таблицу. Чего снимку не нужно, то не читается вовсе.
type projectionBatch struct {
	ports.Tx
	keys         []ids.OfferID
	tenants      []ids.TenantID
	writes       *deferredWrites
	products     *batchProducts
	verdicts     *batchVerdicts
	cases        *batchCases
	sources      *batchSources
	participants *batchParticipants
	taxonomy     *batchTaxonomy
	rules        *batchRules
	offers       *batchOffers
	outbox       *batchOutbox
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

// ProjectionValidation — источники повторного допуска пачки.
func (b *projectionBatch) ProjectionValidation() ports.ProjectionValidation {
	if b.sources == nil {
		repository := b.Tx.ProjectionValidation()
		b.sources = &batchSources{
			ProjectionValidation: repository,
			pending:              prefetched[ports.ValidationSources]{keys: b.keys, read: sourcesReader(repository)},
			writes:               b.writes,
		}
	}
	return b.sources
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
