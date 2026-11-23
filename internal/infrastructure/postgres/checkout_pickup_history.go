package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// pickupHistorySQL читает только колонки выбора по индексу checkouts_buyer_idx: без групп, строк и снимков покупок.
const pickupHistorySQL = `
 SELECT carrier, pickup_point_id, delivery FROM (
  SELECT DISTINCT ON (c.carrier, c.pickup_point_id) c.carrier, c.pickup_point_id, c.delivery, c.created_at
  FROM checkouts c
  WHERE c.buyer_id = $1 AND c.carrier IS NOT NULL AND EXISTS (
   SELECT 1 FROM checkout_groups g
   WHERE g.checkout_id = c.id AND coalesce(g.order_progress->>'OrderID', '') <> ''
  )
  ORDER BY c.carrier, c.pickup_point_id, c.created_at DESC
 ) points
 ORDER BY created_at DESC
 LIMIT $2`

type pickupHistoryRow struct {
	carrier, pointID string
	document         []byte
}

// PickupHistory — пункты, куда покупателю создавались заказы, без повторов, свежие первыми.
func (r *Checkouts) PickupHistory(ctx context.Context, buyerID ids.UUID, limit int) ([]checkout.Selection, error) {
	buyer, err := checkoutFilterUUID(buyerID)
	if err != nil {
		return nil, err
	}

	rows, err := rawSQL(ctx, r.db, pickupHistorySQL, buyer, limit).Rows()
	if err != nil {
		return nil, fmt.Errorf("история пунктов выдачи: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var found []checkout.Selection

	for rows.Next() {
		var row pickupHistoryRow
		if err := rows.Scan(&row.carrier, &row.pointID, &row.document); err != nil {
			return nil, fmt.Errorf("история пунктов выдачи: %w", err)
		}

		selection, err := row.selection()
		if err != nil {
			return nil, err
		}

		found = append(found, selection)
	}

	return found, rows.Err()
}

func (r pickupHistoryRow) selection() (checkout.Selection, error) {
	var doc selectionJSON
	if len(r.document) > 0 {
		if err := json.Unmarshal(r.document, &doc); err != nil {
			return checkout.Selection{}, fmt.Errorf("пункт выдачи %s: %w", r.pointID, err)
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
