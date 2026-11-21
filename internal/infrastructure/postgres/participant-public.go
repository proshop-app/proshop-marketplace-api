package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const scanParticipantIDs = `SELECT tenant_id FROM participants WHERE tenant_id > $1 ORDER BY tenant_id LIMIT $2`

const markSettingsChecked = `UPDATE participants SET settings_synced_at = $3
    WHERE tenant_id = $1 AND version = $2`

// ScanIDs обходит участников без глубокого OFFSET, COUNT и загрузки профилей.
func (r *Participants) ScanIDs(ctx context.Context, after ids.TenantID, limit int) ([]ids.TenantID, error) {
	rows, err := rawSQL(ctx, r.db, scanParticipantIDs, string(after), limit).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := []ids.TenantID{}
	for rows.Next() {
		var tenant ids.TenantID
		if err := rows.Scan(&tenant); err != nil {
			return nil, err
		}
		result = append(result, tenant)
	}
	return result, rows.Err()
}

// SettingsChecked сохраняет обещанный оператору heartbeat без перезаписи неизменившегося профиля.
func (r *Participants) SettingsChecked(ctx context.Context, p *participant.Participant) error {
	state := p.State()
	affected, err := execSQL(
		ctx,
		r.db,
		markSettingsChecked,
		string(p.TenantID()),
		p.Origin().Version,
		state.SettingsSyncedAt,
	)
	if err != nil {
		return err
	}
	if affected == 0 {
		return participant.ErrConflict
	}
	return nil
}
