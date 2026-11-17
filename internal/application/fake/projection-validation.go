package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type projectionValidation struct {
	d *data
}

func (t *tx) ProjectionValidation() ports.ProjectionValidation {
	return projectionValidation{d: t.data}
}

func (r projectionValidation) Current(
	_ context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (ports.ValidationSources, error) {
	member, exists := r.d.participants[tenant]
	sources := ports.ValidationSources{
		TaxonomyVersion: r.d.taxonomy.Version,
		RulesRevision:   r.d.moderation.revision,
		Participant:     exists,
		Probation:       member.Trust == participant.Probation || member.Trust == "",
	}
	if state, ok := r.d.moderation.verdicts[productKey{tenant: tenant, product: product}]; ok {
		sources.Verdict = moderation.RestoreVerdict(state)
	}
	return sources, nil
}
