package postgres

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const readValidationSources = `SELECT th.version, mr.revision, pa.tenant_id IS NOT NULL,
	COALESCE(pa.trust = 'probation', false),
	CASE WHEN mv.tenant_id IS NULL THEN NULL ELSE
		to_jsonb(mv) || jsonb_build_object('operator', jsonb_build_object(
			'id', mv.operator_id, 'name', mv.operator_name))
	END
	FROM taxonomy_head th CROSS JOIN moderation_rules mr
	LEFT JOIN participants pa ON pa.tenant_id = $1
	LEFT JOIN moderation_verdicts mv ON mv.tenant_id = $1 AND mv.product_id = $2
	WHERE th.singleton AND mr.id = 1
	FOR SHARE OF th, mr`

// ProjectionValidation читает только метаданные допуска и прежний вердикт.
type ProjectionValidation struct {
	db *gorm.DB
}

// Current удерживает FOR SHARE обеих голов, не читая definition и список фраз.
func (r *ProjectionValidation) Current(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (ports.ValidationSources, error) {
	var sources ports.ValidationSources
	var raw []byte
	err := rawSQL(ctx, r.db, readValidationSources, tenant, product).Row().Scan(
		&sources.TaxonomyVersion,
		&sources.RulesRevision,
		&sources.Participant,
		&sources.Probation,
		&raw,
	)
	if err != nil || len(raw) == 0 {
		return sources, err
	}
	var state moderation.VerdictState
	if err := json.Unmarshal(raw, &state); err != nil {
		return sources, err
	}
	sources.Verdict = moderation.RestoreVerdict(state)
	return sources, nil
}
