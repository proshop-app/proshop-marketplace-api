package postgres

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// selectVerdictBatch читает вердикты списка ключей по первичному ключу одним запросом.
	selectVerdictBatch = `SELECT ` + verdictColumns + `
		FROM moderation_verdicts
		WHERE (tenant_id, product_id) IN (SELECT * FROM unnest($1::text[], $2::text[]))`

	// saveVerdicts заводит новые вердикты и обновляет прежние условно по исходным версии и статусу
	// одним оператором — и для одного, и для пачки. Причины и решение приходят текстом JSON:
	// пустое значение — jsonb null, как в колонке NOT NULL reasons.
	saveVerdicts = `
		WITH rows AS (
			SELECT * FROM unnest(
				$1::text[], $2::text[], $3::bigint[], $4::bigint[], $5::bigint[], $6::text[], $7::text[],
				$8::text[], $9::boolean[], $10::bigint[], $11::timestamptz[], $12::text[], $13::text[],
				$14::text[], $15::bigint[], $16::text[]
			) AS r(tenant_id, product_id, revision, rules_revision, taxonomy_version, status, reasons,
				decided_by, manual_reject, version, updated_at, operator_id, operator_name, decision, origin,
				origin_status)
		), inserted AS (
			INSERT INTO moderation_verdicts (
				tenant_id,
				product_id,
				revision,
				rules_revision,
				taxonomy_version,
				status,
				reasons,
				decided_by,
				manual_reject,
				version,
				updated_at,
				operator_id,
				operator_name,
				decision
			)
			SELECT tenant_id, product_id, revision, rules_revision, taxonomy_version, status, reasons::jsonb,
				decided_by, manual_reject, version, updated_at, operator_id, operator_name, decision::jsonb
			FROM rows
			WHERE origin = 0
			ON CONFLICT DO NOTHING
			RETURNING 1
		), updated AS (
			UPDATE moderation_verdicts v
			SET revision = r.revision,
				rules_revision = r.rules_revision,
				taxonomy_version = r.taxonomy_version,
				status = r.status,
				reasons = r.reasons::jsonb,
				decided_by = r.decided_by,
				manual_reject = r.manual_reject,
				version = r.version,
				updated_at = r.updated_at,
				operator_id = r.operator_id,
				operator_name = r.operator_name,
				decision = r.decision::jsonb
			FROM rows r
			WHERE r.origin > 0 AND v.tenant_id = r.tenant_id AND v.product_id = r.product_id
				AND v.version = r.origin AND v.status = r.origin_status
			RETURNING 1
		)
		SELECT (SELECT count(*) FROM inserted) + (SELECT count(*) FROM updated)`
)

// verdictWrites — колонки записи вердиктов; одна строка на вердикт, порядок — по ключу.
type verdictWrites struct {
	tenants, products                     []string
	revisions, rulesRevisions, taxonomies []int64
	statuses, reasons, decidedBy          []string
	manualRejects                         []bool
	versions                              []int64
	updated                               []time.Time
	operatorIDs, operatorNames, decisions []string
	origins                               []int64
	originStatuses                        []string
}

// Save не допускает тихой перезаписи конкурентного изменения — пачкой из одного.
func (r *ModerationVerdicts) Save(ctx context.Context, value *moderation.Verdict) error {
	return r.SaveMany(ctx, []*moderation.Verdict{value})
}

// SaveMany пишет вердикты одним оператором в порядке ключа; конфликт любого — ErrConflict.
func (r *ModerationVerdicts) SaveMany(ctx context.Context, verdicts []*moderation.Verdict) error {
	if len(verdicts) == 0 {
		return nil
	}
	sorted := slices.Clone(verdicts)
	slices.SortFunc(sorted, func(a, b *moderation.Verdict) int {
		left, right := a.State(), b.State()
		return cmp.Or(cmp.Compare(left.TenantID, right.TenantID), cmp.Compare(left.ProductID, right.ProductID))
	})
	writes, err := verdictWritesOf(sorted)
	if err != nil {
		return err
	}
	var written int64
	if err := rawSQL(ctx, r.db, saveVerdicts, writes.args()...).Row().Scan(&written); err != nil {
		return fmt.Errorf("вердикты: %w", err)
	}
	if written != int64(len(sorted)) {
		return moderation.ErrConflict
	}
	for _, verdict := range sorted {
		verdict.MarkSaved()
	}
	return nil
}

// GetMany читает вердикты списка одним запросом по первичному ключу.
func (r *ModerationVerdicts) GetMany(
	ctx context.Context,
	keys []ids.OfferID,
) (map[ids.OfferID]*moderation.Verdict, error) {
	tenants, products := offerKeyColumns(keys)
	rows, err := rawSQL(ctx, r.db, selectVerdictBatch, tenants, products).Rows()
	if err != nil {
		return nil, fmt.Errorf("пачка вердиктов: %w", err)
	}
	defer func() { _ = rows.Close() }()
	found := make(map[ids.OfferID]*moderation.Verdict, len(keys))
	for rows.Next() {
		verdict, err := scanModerationVerdict(rows)
		if err != nil {
			return nil, fmt.Errorf("пачка вердиктов: %w", err)
		}
		state := verdict.State()
		found[ids.OfferID{Tenant: state.TenantID, Product: state.ProductID}] = verdict
	}
	return found, rows.Err()
}

func verdictWritesOf(verdicts []*moderation.Verdict) (verdictWrites, error) {
	var w verdictWrites
	for _, verdict := range verdicts {
		s, origin := verdict.State(), verdict.Origin()
		reasons, err := json.Marshal(s.Reasons)
		if err != nil {
			return verdictWrites{}, err
		}
		decision, err := json.Marshal(s.Decision)
		if err != nil {
			return verdictWrites{}, err
		}
		w.tenants = append(w.tenants, string(s.TenantID))
		w.products = append(w.products, string(s.ProductID))
		w.revisions = append(w.revisions, s.Revision)
		w.rulesRevisions = append(w.rulesRevisions, s.RulesRevision)
		w.taxonomies = append(w.taxonomies, s.TaxonomyVersion)
		w.statuses = append(w.statuses, string(s.Status))
		w.reasons = append(w.reasons, string(reasons))
		w.decidedBy = append(w.decidedBy, s.DecidedBy)
		w.manualRejects = append(w.manualRejects, s.ManualReject)
		w.versions = append(w.versions, s.Version)
		w.updated = append(w.updated, s.UpdatedAt)
		w.operatorIDs = append(w.operatorIDs, s.Operator.ID)
		w.operatorNames = append(w.operatorNames, s.Operator.Name)
		w.decisions = append(w.decisions, string(decision))
		w.origins = append(w.origins, origin.Version)
		w.originStatuses = append(w.originStatuses, string(origin.Status))
	}
	return w, nil
}

func (w verdictWrites) args() []any {
	return []any{
		w.tenants,
		w.products,
		w.revisions,
		w.rulesRevisions,
		w.taxonomies,
		w.statuses,
		w.reasons,
		w.decidedBy,
		w.manualRejects,
		w.versions,
		w.updated,
		w.operatorIDs,
		w.operatorNames,
		w.decisions,
		w.origins,
		w.originStatuses,
	}
}
