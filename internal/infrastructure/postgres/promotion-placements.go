package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Значения контракта продвижения не зависят от конкретного магазина.
const (
	promotionPlacementColumns = `code,surface,title,positions,slot_limit,default_price,
 position_prices,active,scope,version`
	promotionPlacements      = `SELECT ` + promotionPlacementColumns + ` FROM promotion_placements ORDER BY code`
	promotionPlacement       = `SELECT ` + promotionPlacementColumns + ` FROM promotion_placements WHERE code=$1`
	promotionLock            = `SELECT code FROM promotion_placements WHERE code=$1 FOR UPDATE`
	promotionPlacementUpdate = `UPDATE promotion_placements SET
 slot_limit=$2,
 default_price=$3,
 position_prices=$4,
 active=$5,
 version=$6
 WHERE code=$1 AND version=$7`
)

// LockPlacement сериализует проверку ёмкости и запись заявки.
func (r *Promotions) LockPlacement(ctx context.Context, code string) error {
	var found string
	err := rawSQL(ctx, r.db, promotionLock, code).Row().Scan(&found)
	if notFound(err) {
		return promotion.ErrNotFound
	}
	return err
}

func scanPromotionPlacement(row rowScanner) (*promotion.Placement, error) {
	var s promotion.PlacementState
	var price int64
	var raw []byte
	err := row.Scan(
		&s.Code,
		&s.Surface,
		&s.Title,
		pgtype.NewMap().SQLScanner(&s.Positions),
		&s.SlotLimit,
		&price,
		&raw,
		&s.Active,
		&s.Scope,
		&s.Version,
	)
	if notFound(err) {
		return nil, promotion.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	prices := map[int]int64{}
	if err := json.Unmarshal(raw, &prices); err != nil {
		return nil, err
	}
	s.DefaultPrice = money.FromKopecks(price)
	s.PositionPrices = map[int]money.Money{}
	for position, value := range prices {
		s.PositionPrices[position] = money.FromKopecks(value)
	}
	return promotion.RestorePlacement(s), nil
}

// Placement читает исходный снимок прайса.
func (r *Promotions) Placement(ctx context.Context, code string) (*promotion.Placement, error) {
	return scanPromotionPlacement(rawSQL(ctx, r.db, promotionPlacement, code).Row())
}

// Placements возвращает прайс с фильтром доступных мест.
func (r *Promotions) Placements(ctx context.Context) ([]promotion.PlacementState, error) {
	rows, err := rawSQL(ctx, r.db, promotionPlacements).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := []promotion.PlacementState{}
	for rows.Next() {
		p, err := scanPromotionPlacement(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p.State())
	}
	return result, rows.Err()
}

// SavePlacement обнаруживает параллельное изменение прайса.
func (r *Promotions) SavePlacement(ctx context.Context, p *promotion.Placement) error {
	s := p.State()
	prices := map[int]int64{}
	for position, value := range s.PositionPrices {
		prices[position] = value.Kopecks()
	}
	raw, err := json.Marshal(prices)
	if err != nil {
		return err
	}
	tag, err := execSQL(
		ctx,
		r.db,
		promotionPlacementUpdate,
		s.Code,
		s.SlotLimit,
		s.DefaultPrice.Kopecks(),
		raw,
		s.Active,
		s.Version,
		p.OriginVersion(),
	)
	if err != nil {
		return err
	}
	if tag == 0 {
		return promotion.ErrConflict
	}
	p.MarkSaved()
	return nil
}
