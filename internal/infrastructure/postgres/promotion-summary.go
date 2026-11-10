package postgres

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	promotionReportGroups = `SELECT placement_code,category_id,status,to_char(date_from,'YYYY-MM-DD'),
 to_char(date_to,'YYYY-MM-DD'),price,count(*) FROM promotion_campaigns
 WHERE date_to >= $1::text::date AND date_from <= $2::text::date
 GROUP BY placement_code,category_id,status,date_from,date_to,price`

	promotionDelete    = `DELETE FROM promotion_campaigns WHERE id=$1 AND status='draft' AND version=$2`
	promotionCounts    = `SELECT status,count(*) FROM promotion_campaigns` + promotionWhere + ` GROUP BY status`
	promotionSummaries = `SELECT c.id,coalesce(p.name,''),coalesce(s.impressions,0),coalesce(s.clicks,0)
 FROM promotion_campaigns c LEFT JOIN participants p ON p.tenant_id=c.tenant_id
 LEFT JOIN (SELECT campaign_id,sum(impressions) impressions,sum(clicks) clicks FROM promotion_counters
 WHERE campaign_id=ANY($1::uuid[]) GROUP BY campaign_id) s ON s.campaign_id=c.id
 WHERE c.id=ANY($1::uuid[])`
	promotionTraffic = `SELECT to_char(day,'YYYY-MM-DD'),sum(impressions),sum(clicks) FROM promotion_counters
 WHERE day>= $1::text::date AND day<= $2::text::date GROUP BY day ORDER BY day`
)

// Delete удаляет только прочитанный черновик; журнал удаляется внешним ключом.
func (r *Promotions) Delete(ctx context.Context, c *promotion.Campaign) error {
	if c.State().Status != promotion.Draft {
		return promotion.ErrConflict
	}
	tag, err := execSQL(ctx, r.db, promotionDelete, c.State().ID, c.Origin().Version)
	if err != nil {
		return err
	}
	if tag == 0 {
		return promotion.ErrConflict
	}
	return nil
}

// Counts считает все совпадения фильтра без пагинации.
func (r *Promotions) Counts(ctx context.Context, f promotion.Filter) (map[promotion.Status]int, error) {
	args := []any{f.Status, f.Kind, f.TenantID, f.PlacementCode, f.From, f.To, f.ProductID}
	rows, err := rawSQL(ctx, r.db, promotionCounts, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := map[promotion.Status]int{}
	for rows.Next() {
		var status promotion.Status
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		result[status] = count
	}
	return result, rows.Err()
}

// Summaries агрегирует счётчики страницы одним запросом вместе с именами магазинов.
func (r *Promotions) Summaries(ctx context.Context, campaignIDs []ids.UUID) (map[ids.UUID]promotion.Summary, error) {
	rows, err := rawSQL(ctx, r.db, promotionSummaries, campaignIDs).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := map[ids.UUID]promotion.Summary{}
	for rows.Next() {
		var id ids.UUID
		var summary promotion.Summary
		if err := rows.Scan(&id, &summary.TenantName, &summary.Impressions, &summary.Clicks); err != nil {
			return nil, err
		}
		result[id] = summary
	}
	return result, rows.Err()
}

// Traffic агрегирует трафик всех кампаний по дням.
func (r *Promotions) Traffic(ctx context.Context, from, to string) ([]promotion.Counter, error) {
	rows, err := rawSQL(ctx, r.db, promotionTraffic, from, to).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := []promotion.Counter{}
	for rows.Next() {
		var row promotion.Counter
		if err := rows.Scan(&row.Day, &row.Impressions, &row.Clicks); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// ReportGroups сводит размещения без загрузки jsonb и отдельных кампаний.
func (r *Promotions) ReportGroups(ctx context.Context, from, to string) ([]promotion.ReportGroup, error) {
	rows, err := rawSQL(ctx, r.db, promotionReportGroups, from, to).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := []promotion.ReportGroup{}
	for rows.Next() {
		var group promotion.ReportGroup
		var price int64
		err := rows.Scan(
			&group.PlacementCode,
			&group.Target.CategoryID,
			&group.Status,
			&group.Period.From,
			&group.Period.To,
			&price,
			&group.Count,
		)
		if err != nil {
			return nil, err
		}
		group.Price = money.FromKopecks(price)
		result = append(result, group)
	}
	return result, rows.Err()
}
