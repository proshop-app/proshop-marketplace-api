package fake

import (
	"context"
	"slices"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (r participants) PublicTenants(
	ctx context.Context,
	filter []ids.TenantID,
	after ids.TenantID,
	limit int,
) ([]ids.TenantID, error) {
	result := []ids.TenantID{}
	for tenant, p := range r.d.participants {
		if tenant <= after || (filter != nil && !slices.Contains(filter, tenant)) {
			continue
		}
		t, err := (registry{r.d}).Get(ctx, tenant)
		if err == nil && participant.VisibilityOf(participant.Restore(p), t).Visible {
			result = append(result, tenant)
		}
	}
	slices.Sort(result)
	return result[:min(limit, len(result))], nil
}
