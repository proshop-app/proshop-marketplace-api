package postgres

import (
	"context"
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Значения контракта продвижения не зависят от конкретного магазина.
const (
	promotionDecisionAdd = `INSERT INTO promotion_decisions(campaign_id,at,actor,action,note) VALUES($1,$2,$3,$4,$5)`
	promotionDecisions   = `SELECT at,actor,action,note FROM promotion_decisions WHERE campaign_id=$1 ORDER BY id`
	// Сначала дедуп, затем счётчики. В каждой фазе блокировки идут в порядке ключей.
	// Один шард на пачку по первому eventId: одна запись счётчика на кампанию и день.
	promotionCount = `
		WITH accepted AS (
			INSERT INTO promotion_views (campaign_id, day, event_id, kind)
			SELECT c.id, $1::text::date, e."eventId", e.kind
			FROM jsonb_to_recordset($2::jsonb) AS e("promotionId" uuid, "eventId" uuid, kind text)
			JOIN promotion_campaigns c ON c.id = e."promotionId"
			WHERE c.status = 'active' AND $1::text::date BETWEEN c.date_from AND c.date_to
			ORDER BY c.id, e."eventId", e.kind
			ON CONFLICT DO NOTHING
			RETURNING campaign_id, day, kind
		), counted AS (
			INSERT INTO promotion_counters (campaign_id, day, position, shard, impressions, clicks)
			SELECT a.campaign_id, a.day, c.position,
				(hashtextextended($2::jsonb -> 0 ->> 'eventId', 0) & 63)::smallint,
				count(*) FILTER (WHERE a.kind = 'view'),
				count(*) FILTER (WHERE a.kind = 'click')
			FROM accepted a
			JOIN promotion_campaigns c ON c.id = a.campaign_id
			GROUP BY a.campaign_id, a.day, c.position
			ORDER BY a.campaign_id, a.day, c.position
			ON CONFLICT (campaign_id, day, position, shard) DO UPDATE
			SET impressions = promotion_counters.impressions + EXCLUDED.impressions,
				clicks = promotion_counters.clicks + EXCLUDED.clicks
		)
		SELECT c.tenant_id, c.placement_code, a.kind
		FROM accepted a
		JOIN promotion_campaigns c ON c.id = a.campaign_id`
	promotionStats = `SELECT to_char(day,'YYYY-MM-DD'),position,sum(impressions),sum(clicks) FROM promotion_counters
 WHERE campaign_id=$1 AND ($2='' OR day>=NULLIF($2,'')::date) AND ($3='' OR day<=NULLIF($3,'')::date)
 GROUP BY day,position ORDER BY day,position`
)

// AddDecision добавляет запись аудита в транзакции перехода.
func (r *Promotions) AddDecision(ctx context.Context, id ids.UUID, d promotion.Decision) error {
	_, err := execSQL(ctx, r.db, promotionDecisionAdd, id, d.At, d.Actor, d.Action, d.Note)
	return err
}

// Decisions возвращает журнал в порядке решений.
func (r *Promotions) Decisions(ctx context.Context, id ids.UUID) ([]promotion.Decision, error) {
	rows, err := rawSQL(ctx, r.db, promotionDecisions, id).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
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

// CountEvents считает активные кампании и записывает всю пачку одним SQL.
func (r *Promotions) CountEvents(
	ctx context.Context,
	day string,
	events []promotion.Beacon,
) ([]promotion.CountedBeacon, error) {
	payload, err := json.Marshal(events)
	if err != nil {
		return nil, err
	}
	rows, err := rawSQL(ctx, r.db, promotionCount, day, payload).Rows()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var counted []promotion.CountedBeacon
	for rows.Next() {
		var event promotion.CountedBeacon
		if err := rows.Scan(&event.TenantID, &event.PlacementCode, &event.Kind); err != nil {
			return nil, err
		}
		counted = append(counted, event)
	}
	return counted, rows.Err()
}

// Stats читает суточные счётчики за указанный период.
func (r *Promotions) Stats(ctx context.Context, id ids.UUID, from, to string) ([]promotion.Counter, error) {
	rows, err := rawSQL(ctx, r.db, promotionStats, id, from, to).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
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
