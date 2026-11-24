package postgres

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// readValidationSources читает головы таксономии и правил один раз под FOR SHARE, участника и
// прежний вердикт — для каждого товара списка.
const readValidationSources = `SELECT k.tenant_id, k.product_id, th.version, mr.revision, pa.tenant_id IS NOT NULL,
	COALESCE(pa.trust = 'probation', false),
	CASE WHEN mv.tenant_id IS NULL THEN NULL ELSE
		to_jsonb(mv) || jsonb_build_object('operator', jsonb_build_object(
			'id', mv.operator_id, 'name', mv.operator_name))
	END
	FROM taxonomy_head th
	CROSS JOIN moderation_rules mr
	CROSS JOIN unnest($1::text[], $2::text[]) AS k(tenant_id, product_id)
	LEFT JOIN participants pa ON pa.tenant_id = k.tenant_id
	LEFT JOIN moderation_verdicts mv ON mv.tenant_id = k.tenant_id AND mv.product_id = k.product_id
	WHERE th.singleton AND mr.id = 1
	FOR SHARE OF th, mr`

// ProjectionValidation читает только метаданные допуска и прежний вердикт.
type ProjectionValidation struct {
	db *gorm.DB
}

// Current — источники допуска одного товара, пачкой из одного.
func (r *ProjectionValidation) Current(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (ports.ValidationSources, error) {
	key := ids.OfferID{Tenant: tenant, Product: product}
	found, err := r.CurrentMany(ctx, []ids.OfferID{key})
	return found[key], err
}

// CurrentMany удерживает FOR SHARE обеих голов, не читая definition и список фраз.
func (r *ProjectionValidation) CurrentMany(
	ctx context.Context,
	keys []ids.OfferID,
) (map[ids.OfferID]ports.ValidationSources, error) {
	tenants, products := offerKeyColumns(keys)
	rows, err := rawSQL(ctx, r.db, readValidationSources, tenants, products).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	found := make(map[ids.OfferID]ports.ValidationSources, len(keys))
	for rows.Next() {
		var tenant, product string
		var sources ports.ValidationSources
		var raw []byte
		err := rows.Scan(
			&tenant,
			&product,
			&sources.TaxonomyVersion,
			&sources.RulesRevision,
			&sources.Participant,
			&sources.Probation,
			&raw,
		)
		if err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			var state moderation.VerdictState
			if err := json.Unmarshal(raw, &state); err != nil {
				return nil, err
			}
			sources.Verdict = moderation.RestoreVerdict(state)
		}
		found[ids.OfferID{Tenant: ids.TenantID(tenant), Product: ids.ProductID(product)}] = sources
	}
	return found, rows.Err()
}
