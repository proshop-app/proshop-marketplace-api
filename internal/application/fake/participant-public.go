package fake

import (
	"context"
	"slices"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
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
		t, err := registry(r).Get(ctx, tenant)
		if err == nil && participant.VisibilityOf(participant.Restore(p), t).Visible {
			result = append(result, tenant)
		}
	}
	slices.Sort(result)
	return result[:min(limit, len(result))], nil
}

func (r participants) ScanIDs(_ context.Context, after ids.TenantID, limit int) ([]ids.TenantID, error) {
	result := []ids.TenantID{}
	for tenant := range r.d.participants {
		if tenant > after {
			result = append(result, tenant)
		}
	}
	slices.Sort(result)
	return result[:min(limit, len(result))], nil
}

func (r participants) SettingsChecked(_ context.Context, p *participant.Participant) error {
	state, ok := r.d.participants[p.TenantID()]
	if !ok || state.Version != p.Origin().Version {
		return participant.ErrConflict
	}
	state.SettingsSyncedAt = p.State().SettingsSyncedAt
	r.d.participants[p.TenantID()] = state
	return nil
}

func (r registry) MissingParticipants(_ context.Context, after ids.TenantID, limit int) ([]*participant.Tenant, error) {
	keys := []ids.TenantID{}
	for tenant, state := range r.d.tenants {
		_, exists := r.d.participants[tenant]
		if !exists && tenant > after && state.Status == participant.TenantActive {
			keys = append(keys, tenant)
		}
	}
	slices.Sort(keys)
	result := []*participant.Tenant{}
	for _, tenant := range keys[:min(limit, len(keys))] {
		result = append(result, participant.RestoreTenant(r.d.tenants[tenant]))
	}
	return result, nil
}

// PickupSources повторяет отбор хранилища без выдачи агрегатов вызывающему.
func (r participants) PickupSources(
	ctx context.Context,
	carrier string,
	after ids.TenantID,
	limit int,
) ([]ids.TenantID, error) {
	keys, err := r.PublicTenants(ctx, nil, after, len(r.d.participants))
	if err != nil {
		return nil, err
	}
	result := []ids.TenantID{}
	for _, key := range keys {
		if r.d.participants[key].Enabled && slices.ContainsFunc(delivery.Carriers(), func(item delivery.CarrierInfo) bool {
			return string(item.Code) == carrier
		}) {
			result = append(result, key)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}
