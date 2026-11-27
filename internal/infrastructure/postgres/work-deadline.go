package postgres

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	// В обычной полосе крайняя запись индекса даёт срок сразу. Если первая запись
	// принадлежит занятому магазину или не является головой упорядоченной полосы,
	// обходим магазины прыжками по индексу, не перебирая хвосты их задач.
	nextOutboxWork = `WITH RECURSIVE` + claimQueuedKinds + `,
		kinds AS (
			SELECT kind FROM unnest($1::text[]) kind
			UNION ALL
			SELECT kind FROM claim_queued_kinds WHERE cardinality($1::text[]) = 0
		), first_task AS MATERIALIZED (
			SELECT k.kind, first.tenant_id, first.id, first.next_at, k.kind = ANY($9::text[]) AS ordered
			FROM kinds k
			CROSS JOIN LATERAL (
				SELECT id, tenant_id, next_at FROM outbox
				WHERE kind = k.kind AND failed_at IS NULL ORDER BY next_at, id LIMIT 1
			) first WHERE NOT (k.kind = ANY($2::text[]))
		), checked AS MATERIALIZED (
			SELECT f.*, COALESCE(($4::jsonb ->> f.tenant_id)::int, $7) > 0
				AND (NOT f.ordered OR f.id = (
					SELECT id FROM outbox
					WHERE kind = f.kind AND kind IN ` + orderedLaneKinds + `
						AND tenant_id = f.tenant_id AND failed_at IS NULL
					ORDER BY id LIMIT 1
				)) AS usable
			FROM first_task f
		), lanes(kind, position, wrapped, visited, next_at) AS (
			SELECT kind, $8::text, false, 0, NULL::timestamptz FROM checked WHERE NOT usable
			UNION ALL
			SELECT l.kind, n.tenant_id, n.wrapped, l.visited + 1, head.next_at
			FROM lanes l
			CROSS JOIN LATERAL (` + claimNextLane + `
			) n
			CROSS JOIN LATERAL (` + claimLaneHead + `
			) head
			WHERE l.visited < $3 AND NOT (n.wrapped AND ($8::text IS NULL OR n.tenant_id > $8))
		), deadlines AS (
			SELECT kind, next_at FROM checked WHERE usable
			UNION ALL
			SELECT kind, next_at FROM lanes
			WHERE visited > 0 AND COALESCE(($4::jsonb ->> position)::int, $7) > 0
			UNION ALL
			-- За пределами просмотренного окна возможна более ранняя голова.
			-- Нижняя граница будит взятие, которое продвинет тот же курсор.
			SELECT f.kind, f.next_at FROM checked f JOIN lanes l USING (kind)
			WHERE l.visited = $3
		)
		SELECT kind, min(CASE WHEN kind = ANY($6::text[]) THEN greatest(next_at,
			(SELECT resume_at FROM outbox_lane_pauses WHERE lane = $5)) ELSE next_at END)
		FROM deadlines WHERE next_at IS NOT NULL GROUP BY kind`

	nextTaxonomyWork = `SELECT (SELECT available_at FROM taxonomy_revalidation WHERE NOT done
		ORDER BY available_at, version, category LIMIT 1)`
)

// NextWorkByKind снимает ближайшие сроки всех запрошенных видов одним оператором.
func (q *TaskQueue) NextWorkByKind(ctx context.Context, request ports.ClaimRequest) (map[string]time.Time, error) {
	for _, kind := range request.OrderedKinds {
		if !slices.Contains(OrderedTaskKinds, kind) {
			return nil, ErrUnorderedLane.WithMessage("вид " + kind)
		}
	}
	slots, err := json.Marshal(request.TenantSlots)
	if err != nil {
		return nil, err
	}
	next := map[string]time.Time{}
	err = q.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		rows, err := rawSQL(
			ctx,
			db,
			nextOutboxWork,
			append([]string{}, request.Kinds...),
			append([]string{}, request.ExcludeKinds...),
			ports.ClaimLaneVisits,
			string(slots),
			request.Gate.Key,
			append([]string{}, request.Gate.Kinds...),
			max(1, request.PerTenant),
			claimAfter(request.After),
			append([]string{}, request.OrderedKinds...),
		).Rows()
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var kind string
			var at time.Time
			if err := rows.Scan(&kind, &at); err != nil {
				return err
			}
			next[kind] = at
		}
		return rows.Err()
	})
	return next, err
}

// NextTaxonomyWork читает срок ближайшей незавершённой страницы по индексу.
func (q *TaskQueue) NextTaxonomyWork(ctx context.Context) (time.Time, error) {
	var next *time.Time
	err := q.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		return rawSQL(ctx, db, nextTaxonomyWork).Row().Scan(&next)
	})
	return workTime(next), err
}

func workTime(next *time.Time) time.Time {
	if next == nil {
		return time.Time{}
	}
	return *next
}
