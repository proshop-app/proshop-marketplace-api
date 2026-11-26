package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Магазин для диагностики читается только у покупок, уже вошедших в ограниченную выдачу.
const pickupHistorySQL = `
 SELECT id, carrier, pickup_point_id, delivery, (
  SELECT g.tenant_id FROM checkout_groups g
  WHERE g.checkout_id = recent.id AND coalesce(g.order_progress->>'OrderID', '') <> ''
  ORDER BY g.tenant_id LIMIT 1
 ) FROM (
  SELECT * FROM (
   SELECT DISTINCT ON (c.carrier, c.pickup_point_id) c.id, c.carrier, c.pickup_point_id, c.delivery, c.created_at
   FROM checkouts c
   WHERE c.buyer_id = $1 AND c.carrier IS NOT NULL AND EXISTS (
    SELECT 1 FROM checkout_groups g
    WHERE g.checkout_id = c.id AND coalesce(g.order_progress->>'OrderID', '') <> ''
   )
   ORDER BY c.carrier, c.pickup_point_id, c.created_at DESC
  ) points
  ORDER BY created_at DESC
  LIMIT $2
 ) recent
 ORDER BY created_at DESC`

type pickupHistoryRow struct {
	id               ids.UUID
	tenant           ids.TenantID
	carrier, pointID string
	document         []byte
}

// PickupHistory — пункты, куда покупателю создавались заказы, без повторов, свежие первыми.
func (r *Checkouts) PickupHistory(
	ctx context.Context,
	buyerID ids.UUID,
	limit int,
) (checkout.PickupHistoryPage, error) {
	buyer, err := checkoutFilterUUID(buyerID)
	if err != nil {
		return checkout.PickupHistoryPage{}, err
	}

	rows, err := rawSQL(ctx, r.db, pickupHistorySQL, buyer, limit).Rows()
	if err != nil {
		return checkout.PickupHistoryPage{}, fmt.Errorf("история пунктов выдачи: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var page checkout.PickupHistoryPage
	for rows.Next() {
		var row pickupHistoryRow
		if err := rows.Scan(&row.id, &row.carrier, &row.pointID, &row.document, &row.tenant); err != nil {
			return checkout.PickupHistoryPage{}, fmt.Errorf("история пунктов выдачи: %w", err)
		}

		selection, err := row.selection()
		if err != nil {
			page.Failures = append(page.Failures, checkout.ReadFailure{
				ID:       row.id,
				BuyerID:  buyerID,
				TenantID: row.tenant,
				Cause:    err,
				Reason:   checkout.ReadHeader,
			})
			continue
		}

		page.Points = append(page.Points, selection)
	}

	return page, rows.Err()
}

func (r pickupHistoryRow) selection() (checkout.Selection, error) {
	var doc selectionJSON
	if len(r.document) > 0 {
		if err := json.Unmarshal(r.document, &doc); err != nil {
			return checkout.Selection{}, fmt.Errorf("доставка в пункт %s: %w", r.pointID, err)
		}
	}

	return checkout.Selection{
		Carrier:         delivery.Carrier(r.carrier),
		PickupPointCode: r.pointID,
		City:            doc.City,
		Address:         doc.Address,
		RegionCode:      doc.RegionCode,
	}, nil
}
