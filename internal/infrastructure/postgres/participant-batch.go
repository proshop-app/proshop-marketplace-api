package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const participantBatch = `SELECT ` + participantColumns + ` FROM participants WHERE tenant_id = ANY($1::text[])`

// Batch читает полных участников только нужных магазинов одним запросом.
func (r *Participants) Batch(
	ctx context.Context,
	tenants []ids.TenantID,
) (map[ids.TenantID]*participant.Participant, error) {
	values := make([]string, 0, len(tenants))
	for _, tenant := range tenants {
		values = append(values, string(tenant))
	}
	rows, err := rawSQL(ctx, r.db, participantBatch, values).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := map[ids.TenantID]*participant.Participant{}
	for rows.Next() {
		value, err := scanParticipant(rows)
		if err != nil {
			return nil, err
		}
		result[value.TenantID()] = value
	}
	return result, rows.Err()
}
