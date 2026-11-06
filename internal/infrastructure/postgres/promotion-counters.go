package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Значения контракта продвижения не зависят от конкретного магазина.
const (
	promotionDecisionAdd = `INSERT INTO promotion_decisions(campaign_id,at,actor,action,note) VALUES($1,$2,$3,$4,$5)`
	promotionDecisions   = `SELECT at,actor,action,note FROM promotion_decisions WHERE campaign_id=$1 ORDER BY id`
	promotionView        = `INSERT INTO promotion_views(campaign_id,day,visitor_hash,kind)
 VALUES($1,$2::text::date,$3,$4)
 ON CONFLICT DO NOTHING`
	promotionCount = `INSERT INTO promotion_counters(campaign_id,day,position,impressions,clicks)
 VALUES($1,$2::text::date,$3,$4,$5)
 ON CONFLICT(campaign_id,day,position) DO UPDATE SET
 impressions=promotion_counters.impressions+EXCLUDED.impressions,
 clicks=promotion_counters.clicks+EXCLUDED.clicks`
	promotionStats = `SELECT to_char(day,'YYYY-MM-DD'),position,impressions,clicks FROM promotion_counters
 WHERE campaign_id=$1 AND ($2='' OR day>=NULLIF($2,'')::date) AND ($3='' OR day<=NULLIF($3,'')::date)
 ORDER BY day,position`
)

// AddDecision добавляет запись аудита в транзакции перехода.
func (r *Promotions) AddDecision(ctx context.Context, id ids.UUID, d promotion.Decision) error {
	_, err := r.q.Exec(ctx, promotionDecisionAdd, id, d.At, d.Actor, d.Action, d.Note)
	return err
}

// Decisions возвращает журнал в порядке решений.
func (r *Promotions) Decisions(ctx context.Context, id ids.UUID) ([]promotion.Decision, error) {
	rows, err := r.q.Query(ctx, promotionDecisions, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []promotion.Decision{}
	for rows.Next() {
		var d promotion.Decision
		if err := rows.Scan(&d.At, &d.Actor, &d.Action, &d.Note); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

// Count дедуплицирует показ и увеличивает счётчик в одной транзакции.
func (r *Promotions) Count(
	ctx context.Context,
	s promotion.CampaignState,
	day, visitor, kind string,
) (bool, error) {
	views, clicks := 0, 0
	switch kind {
	case "view":
		views = 1
	case "click":
		clicks = 1
	default:
		return false, promotion.ErrInvalid
	}
	tag, err := r.q.Exec(ctx, promotionView, s.ID, day, visitor, kind)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := r.q.Exec(ctx, promotionCount, s.ID, day, s.Position, views, clicks); err != nil {
		return false, err
	}
	return true, nil
}

// Stats читает суточные счётчики за указанный период.
func (r *Promotions) Stats(ctx context.Context, id ids.UUID, from, to string) ([]promotion.Counter, error) {
	rows, err := r.q.Query(ctx, promotionStats, id, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []promotion.Counter{}
	for rows.Next() {
		var c promotion.Counter
		if err := rows.Scan(&c.Day, &c.Position, &c.Impressions, &c.Clicks); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
