package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"
	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	activeRevalidationFailure = `p.revision = f.source_revision
        AND (p.version = f.product_version OR
            CASE WHEN jsonb_typeof(p.card->'publication'->'version') = 'number'
            THEN (p.card->'publication'->>'version')::numeric ELSE 0 END < f.taxonomy_version)`
	readRevalidationModeration = `SELECT k.tenant_id, k.product_id,
        to_jsonb(v) || jsonb_build_object('operator', jsonb_build_object('id', v.operator_id, 'name', v.operator_name)),
        CASE WHEN c.id IS NULL THEN NULL ELSE to_jsonb(c) || jsonb_build_object(
            'tenantId', c.tenant_id, 'productId', c.product_id, 'openedAt', c.opened_at, 'decidedAt', c.decided_at) END,
        ` + signalsColumns + `
        FROM unnest($1::text[], $2::text[]) k(tenant_id, product_id)
        LEFT JOIN moderation_verdicts v USING (tenant_id, product_id)
        LEFT JOIN moderation_cases c ON c.tenant_id = k.tenant_id AND c.product_id = k.product_id AND c.status = 'open'
        LEFT JOIN offers o ON o.tenant_id = k.tenant_id AND o.product_id = k.product_id`
	recordRevalidationFailures = `INSERT INTO taxonomy_revalidation_failures
        (tenant_id, product_id, product_version, source_revision, taxonomy_version, diagnostic, failed_at)
        SELECT x."tenantId", x."productId", x."productVersion", x."sourceRevision",
            x."taxonomyVersion", left(x.diagnostic, $2), x.at
        FROM jsonb_to_recordset($1::jsonb) AS x("tenantId" text, "productId" text,
            "productVersion" bigint, "sourceRevision" bigint, "taxonomyVersion" bigint, diagnostic text, at timestamptz)
        ON CONFLICT (tenant_id, product_id) DO UPDATE
        SET product_version = EXCLUDED.product_version,
            source_revision = EXCLUDED.source_revision,
            taxonomy_version = EXCLUDED.taxonomy_version,
            diagnostic = EXCLUDED.diagnostic,
            failed_at = EXCLUDED.failed_at`
	listRevalidationFailures = `SELECT f.tenant_id, f.product_id, f.product_version, f.source_revision,
        f.taxonomy_version, left(f.diagnostic, $4), f.failed_at FROM taxonomy_revalidation_failures f
        JOIN catalog_products p ON p.tenant_id = f.tenant_id AND p.product_id = f.product_id
            AND ` + activeRevalidationFailure + `
        WHERE (f.tenant_id, f.product_id) > ($1, $2)
        ORDER BY f.tenant_id, f.product_id LIMIT $3`
)

// Revalidations использует только транзакцию страницы, не владеет кэшем или пулом.
type Revalidations struct {
	db *gorm.DB
}

type revalidationSignalTimes struct {
	stock   pgtype.Timestamptz
	price   pgtype.Timestamptz
	updated pgtype.Timestamptz
}

// Page сохраняет ключ повреждённой строки, чтобы ошибка разбора не останавливала категорию.
func (r *Revalidations) Page(
	ctx context.Context,
	job taxonomy.Revalidation,
	limit int,
) ([]ports.RevalidationItem, error) {
	rows, err := rawSQL(ctx, r.db, categoryProducts, job.Category, job.TenantCursor, job.ProductCursor, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []ports.RevalidationItem{}
	for rows.Next() {
		var row productRow
		var projected, updated pgtype.Timestamptz
		if err := rows.Scan(row.targetsWithTimes(&projected, &updated)...); err != nil {
			return nil, err
		}
		item := ports.RevalidationItem{
			Key:      ids.OfferID{Tenant: ids.TenantID(row.tenantID), Product: ids.ProductID(row.productID)},
			Version:  row.version,
			Revision: row.revision,
		}
		if !projected.Valid || !updated.Valid || projected.InfinityModifier != pgtype.Finite ||
			updated.InfinityModifier != pgtype.Finite {
			item.Failure = "decode: non-finite product timestamp"
			items = append(items, item)
			continue
		}
		row.projectedAt, row.updatedAt = projected.Time, updated.Time
		item.Product, err = row.product()
		if err != nil {
			item.Failure = "decode: " + err.Error()
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Moderation читает оценки, открытые дела и сигналы одним запросом, без повторного чтения карточек.
func (r *Revalidations) Moderation(
	ctx context.Context,
	keys []ids.OfferID,
) (map[ids.OfferID]ports.RevalidationModeration, error) {
	tenants, products := []string{}, []string{}
	for _, key := range keys {
		tenants = append(tenants, string(key.Tenant))
		products = append(products, string(key.Product))
	}
	rows, err := rawSQL(ctx, r.db, readRevalidationModeration, tenants, products).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := map[ids.OfferID]ports.RevalidationModeration{}
	for rows.Next() {
		var key ids.OfferID
		var verdict, review []byte
		var signals signalsRow
		var times revalidationSignalTimes
		signalTargets := signals.targetsWithTimes(&times.stock, &times.price, &times.updated)
		targets := append([]any{&key.Tenant, &key.Product, &verdict, &review}, signalTargets...)
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		if !times.apply(&signals) {
			result[key] = ports.RevalidationModeration{Failure: "moderation signals: non-finite timestamp"}
			continue
		}
		result[key] = decodeRevalidationModeration(verdict, review, signals)
	}
	return result, rows.Err()
}

func decodeRevalidationModeration(verdict, review []byte, signals signalsRow) ports.RevalidationModeration {
	result := ports.RevalidationModeration{Signals: signals.signals()}
	if len(verdict) > 0 {
		var state moderation.VerdictState
		if err := json.Unmarshal(verdict, &state); err != nil {
			result.Failure = "moderation verdict: " + err.Error()
			return result
		}
		result.Verdict = moderation.RestoreVerdict(state)
	}
	if len(review) > 0 {
		var state moderation.CaseState
		if err := json.Unmarshal(review, &state); err != nil {
			result.Failure = "moderation case: " + err.Error()
			return result
		}
		result.Case = moderation.RestoreCase(state)
	}
	return result
}

// RecordFailures сохраняет диагностику пачкой после изоляции повреждённых карточек.
func (r *Revalidations) RecordFailures(ctx context.Context, failures []ports.RevalidationFailure) error {
	if len(failures) == 0 {
		return nil
	}
	raw, err := json.Marshal(failures)
	if err != nil {
		return err
	}
	_, err = execSQL(ctx, r.db, recordRevalidationFailures, string(raw), ports.RevalidationDiagnosticLimit)
	return err
}

// Failures — keyset-страница только ещё не исправленных версий исходных карточек.
func (r *Revalidations) Failures(
	ctx context.Context,
	after ports.OfferKey,
	limit int,
) ([]ports.RevalidationFailure, error) {
	rows, err := rawSQL(
		ctx,
		r.db,
		listRevalidationFailures,
		after.TenantID,
		after.ProductID,
		limit,
		ports.RevalidationDiagnosticLimit,
	).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []ports.RevalidationFailure{}
	for rows.Next() {
		var value ports.RevalidationFailure
		if err := rows.Scan(
			&value.TenantID,
			&value.ProductID,
			&value.ProductVersion,
			&value.SourceRevision,
			&value.TaxonomyVersion,
			&value.Diagnostic,
			&value.At,
		); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (t revalidationSignalTimes) apply(row *signalsRow) bool {
	for _, value := range []pgtype.Timestamptz{t.stock, t.price, t.updated} {
		if value.Valid && value.InfinityModifier != pgtype.Finite {
			return false
		}
	}
	if t.stock.Valid {
		row.stockAt = &t.stock.Time
	}
	if t.price.Valid {
		row.priceAt = &t.price.Time
	}
	if t.updated.Valid {
		row.updatedAt = &t.updated.Time
	}
	return true
}
