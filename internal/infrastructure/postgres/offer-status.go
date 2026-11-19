package postgres

import (
	"context"
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const selectOfferStatuses = `SELECT p.tenant_id, p.product_id, p.revision, p.deleted,
    p.card->'publication',
    CASE WHEN v.tenant_id IS NULL THEN NULL ELSE to_jsonb(v) || jsonb_build_object(
        'operator', jsonb_build_object('id', v.operator_id, 'name', v.operator_name)) END,
    d.body, c.reasons
    FROM catalog_products p
    LEFT JOIN moderation_verdicts v ON v.tenant_id = p.tenant_id AND v.product_id = p.product_id
    LEFT JOIN LATERAL (
        SELECT jsonb_build_object('id', d.id, 'caseId', d.case_id,
            'operator', jsonb_build_object('id', d.operator_id, 'name', d.operator_name),
            'action', d.action, 'reason', d.reason, 'reasonMessage', d.reason_message,
            'note', d.note, 'revision', d.revision, 'at', d.at) AS body, d.case_id
        FROM moderation_cases c JOIN moderation_decisions d ON d.case_id = c.id
        WHERE c.tenant_id = p.tenant_id AND c.product_id = p.product_id
        ORDER BY d.at DESC, d.id DESC LIMIT 1
    ) d ON true
    LEFT JOIN moderation_cases c ON c.id = d.case_id
    WHERE (p.tenant_id, p.product_id) IN (SELECT * FROM unnest($1::text[], $2::text[]))`

// StatusBatch не читает профиль, сигналы или реестр, не участвующие в обратной связи.
func (r *Offers) StatusBatch(ctx context.Context, keys []ids.OfferID) ([]ports.OfferStatus, error) {
	tenants, products := make([]string, 0, len(keys)), make([]string, 0, len(keys))
	for _, key := range keys {
		tenants = append(tenants, string(key.Tenant))
		products = append(products, string(key.Product))
	}
	rows, err := rawSQL(ctx, r.db, selectOfferStatuses, tenants, products).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	statuses := make([]ports.OfferStatus, 0, len(keys))
	for rows.Next() {
		var status ports.OfferStatus
		var publication, verdict, decision, reasons []byte
		err := rows.Scan(
			&status.Key.Tenant,
			&status.Key.Product,
			&status.Revision,
			&status.Deleted,
			&publication,
			&verdict,
			&decision,
			&reasons,
		)
		if err != nil {
			return nil, err
		}
		status.Err = decodeOfferStatus(&status, publication, verdict, decision, reasons)
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

func decodeOfferStatus(status *ports.OfferStatus, publication, verdict, decision, reasons []byte) error {
	// Старые карточки могли не содержать publication: как и обычное чтение, считаем их неопубликованными.
	if len(publication) > 0 {
		if err := json.Unmarshal(publication, &status.Publication); err != nil {
			return err
		}
	}
	if len(verdict) > 0 {
		var state moderation.VerdictState
		if err := json.Unmarshal(verdict, &state); err != nil {
			return err
		}
		status.Verdict = moderation.RestoreVerdict(state)
	}
	if len(decision) > 0 {
		if err := json.Unmarshal(decision, &status.Decision); err != nil {
			return err
		}
	}
	if len(reasons) > 0 {
		return json.Unmarshal(reasons, &status.CaseReasons)
	}
	return nil
}
