package postgres

import (
	"context"
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Значения контракта продвижения не зависят от конкретного магазина.
const (
	promotionGet   = `SELECT state, price, payment_amount FROM promotion_campaigns WHERE id=$1`
	promotionWhere = ` WHERE ($1='' OR status=$1) AND ($2='' OR kind=$2) AND ($3='' OR tenant_id=$3)
 AND ($4='' OR placement_code=$4) AND ($5='' OR date_to>=NULLIF($5,'')::date)
 AND ($6='' OR date_from<=NULLIF($6,'')::date)`
	promotionList = `SELECT state, price, payment_amount FROM promotion_campaigns` + promotionWhere +
		` ORDER BY id LIMIT NULLIF($7,0) OFFSET $8`
	promotionTotal  = `SELECT count(*) FROM promotion_campaigns` + promotionWhere
	promotionInsert = `INSERT INTO promotion_campaigns
 (id,tenant_id,kind,placement_code,position,category_id,date_from,date_to,price,payment_amount,status,version,state)
 VALUES ($1,$2,$3,$4,$5,$6,$7::text::date,$8::text::date,$9,$10,$11,$12,$13) ON CONFLICT DO NOTHING`
	promotionUpdate = `UPDATE promotion_campaigns SET
 tenant_id=$2,
 kind=$3,
 placement_code=$4,
 position=$5,
 category_id=$6,
 date_from=$7::text::date,
 date_to=$8::text::date,
 price=$9,
 payment_amount=$10,
 status=$11,
 version=$12,
 state=$13
 WHERE id=$1 AND version=$14 AND status=$15`
	promotionOccupancy = `SELECT w.day, count(c.id)
 FROM unnest($1::text[]) AS w(day) LEFT JOIN promotion_campaigns c
 ON c.placement_code=$2 AND c.position=$3 AND ($4='global' OR c.category_id=$5)
 AND c.id<>$6 AND c.status IN ('review','approved','active') AND c.date_from<=w.day::date AND c.date_to>=w.day::date
 GROUP BY w.day ORDER BY w.day`
)

// Promotions связывает репозитории продвижения одной транзакцией PostgreSQL.
type Promotions struct{ q querier }

func scanPromotion(row rowScanner) (*promotion.Campaign, error) {
	var state promotion.CampaignState
	var raw []byte
	var price, amount int64
	if err := row.Scan(&raw, &price, &amount); err != nil {
		if notFound(err) {
			return nil, promotion.ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	state.Price = money.FromKopecks(price)
	state.Payment.Amount = money.FromKopecks(amount)
	return promotion.RestoreCampaign(state), nil
}

// Get читает размещение с проверкой области доступа.
func (r *Promotions) Get(ctx context.Context, id ids.UUID) (*promotion.Campaign, error) {
	return scanPromotion(r.q.QueryRow(ctx, promotionGet, id))
}

// List возвращает страницу размещений и полное число совпадений.
func (r *Promotions) List(ctx context.Context, f promotion.Filter) ([]promotion.CampaignState, int, error) {
	args := []any{f.Status, f.Kind, f.TenantID, f.PlacementCode, f.From, f.To}
	var total int
	if err := r.q.QueryRow(ctx, promotionTotal, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.q.Query(ctx, promotionList, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []promotion.CampaignState{}
	for rows.Next() {
		campaign, err := scanPromotion(rows)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, campaign.State())
	}
	return result, total, rows.Err()
}

// Save записывает агрегат условно по исходным статусу и версии.
func (r *Promotions) Save(ctx context.Context, c *promotion.Campaign) error {
	s := c.State()
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	args := []any{
		s.ID,
		s.TenantID,
		s.Kind,
		s.PlacementCode,
		s.Position,
		s.Target.CategoryID,
		s.Period.From,
		s.Period.To,
		s.Price.Kopecks(),
		s.Payment.Amount.Kopecks(),
		s.Status,
		s.Version,
		raw,
	}
	query := promotionInsert
	if c.Origin().Version > 0 {
		query = promotionUpdate
		args = append(args, c.Origin().Version, c.Origin().Status)
	}
	tag, err := r.q.Exec(ctx, query, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return promotion.ErrConflict
	}
	c.MarkSaved()
	return nil
}

// Occupancy считает занятые недели без редактируемого размещения.
func (r *Promotions) Occupancy(
	ctx context.Context,
	s promotion.CampaignState,
	scope promotion.Scope,
	weeks []string,
) ([]promotion.Week, error) {
	rows, err := r.q.Query(
		ctx,
		promotionOccupancy,
		weeks,
		s.PlacementCode,
		s.Position,
		scope,
		s.Target.CategoryID,
		s.ID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []promotion.Week{}
	for rows.Next() {
		var week promotion.Week
		if err := rows.Scan(&week.From, &week.Sold); err != nil {
			return nil, err
		}
		result = append(result, week)
	}
	return result, rows.Err()
}
