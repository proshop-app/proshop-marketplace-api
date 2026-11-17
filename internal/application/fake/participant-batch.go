package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (r registry) Batch(_ context.Context, keys []ids.TenantID) (map[ids.TenantID]*participant.Tenant, error) {
	result := map[ids.TenantID]*participant.Tenant{}
	for _, key := range keys {
		if state, ok := r.d.tenants[key]; ok {
			result[key] = participant.RestoreTenant(state)
		}
	}
	return result, nil
}
