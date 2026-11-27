package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Взятие задач читает очередь по полосам «вид × магазин» (outbox_lane_due_idx: вид, магазин,
// срок, id): один спуск по индексу на полосу, хвост задач магазина не читается. Каждый вид
// обходит магазины по кругу от курсора и посещает не больше claimLaneVisits полос — стоимость
// взятия ограничена при любом числе магазинов и любом состоянии очереди. Полоса считается
// готовой, только если реально даёт задачу: у упорядоченного вида — старейшая по id созрела.
//
// Параметры запроса:
//
//	$1  now        — срок созревания задач;
//	$2  limit      — задач в пачке; столько же готовых полос ищет обход вида;
//	$3  per_tenant — квота магазина без слота;
//	$4  lease      — новый срок взятой задачи (конец аренды);
//	$5  kinds      — виды запроса; пусто — все виды очереди;
//	$6  exclude    — виды, которые не брать;
//	$7  slots      — jsonb «магазин → свободные места» (TenantSlots);
//	$8  after      — курсор круга магазинов; NULL — круг не начат (пустая строка — пустой магазин);
//	$9  ordered    — упорядоченные виды запроса (подмножество OrderedTaskKinds);
//	$10 visits     — потолок посещённых полос одного вида;
//	$11 recent     — сначала пробовать магазин из конца очереди;
//	$12 deprioritized — магазины, которые пропускают, если найден свежий кандидат;
//	$13 gate_key    — ключ общей отсрочки;
//	$14 gate_kinds  — виды, на которые действует отсрочка.
const (
	// orderedLaneKinds — упорядоченные виды буквально: так планировщик выбирает частичный индекс
	// outbox_ordered_head_idx для старейшей задачи полосы. Список совпадает с OrderedTaskKinds
	// и предикатом индекса в миграции 00070.
	orderedLaneKinds = `('participant.settings.sync', 'moderation.participant.status', ` +
		`'reference.changed', 'search-synonyms.changed')`

	// claimQueuedKinds — виды, у которых есть незавершённые задачи: один спуск на вид.
	claimQueuedKinds = `
		claim_queued_kinds(kind) AS (
			(SELECT kind FROM outbox WHERE failed_at IS NULL ORDER BY kind LIMIT 1)
			UNION ALL
			SELECT following.kind
			FROM claim_queued_kinds q
			CROSS JOIN LATERAL (
				SELECT kind FROM outbox WHERE failed_at IS NULL AND kind > q.kind ORDER BY kind LIMIT 1
			) following
		)`

	// claimKinds — виды взятия: из запроса или все виды очереди, без исключённых.
	// Для одного явно запрошенного вида сразу проверяем по индексу, есть ли созревшие задачи.
	claimKinds = `
		claim_requested_kinds AS (
			SELECT kind FROM unnest($5::text[]) AS kind
			WHERE cardinality($5::text[]) > 1 OR EXISTS (
				SELECT 1 FROM outbox
				WHERE cardinality($5::text[]) = 1 AND kind = ($5::text[])[1]
					AND NOT (kind = ANY($6::text[])) AND failed_at IS NULL AND next_at <= $1
			)
			UNION
			SELECT kind FROM claim_queued_kinds WHERE cardinality($5::text[]) = 0
		), claim_kinds AS (
			SELECT kind FROM claim_requested_kinds
			WHERE NOT (kind = ANY($14::text[])) OR NOT EXISTS (
				SELECT FROM outbox_lane_pauses WHERE lane = $13 AND resume_at > $1
			)
		)`

	// claimNextLane — следующая полоса вида после l.position; в конце вида — переход к началу
	// круга (wrapped). Ветки упорядочены явно: сначала продолжение, потом переход.
	claimNextLane = `
			SELECT candidate.tenant_id, candidate.next_at, candidate.id, candidate.wrapped
			FROM (
				(SELECT tenant_id, next_at, id, l.wrapped AS wrapped, 1 AS branch
				 FROM outbox
				 WHERE l.position IS NOT NULL AND kind = l.kind AND tenant_id > l.position AND failed_at IS NULL
				 ORDER BY kind, tenant_id, next_at, id
				 LIMIT 1)
				UNION ALL
				(SELECT tenant_id, next_at, id, l.position IS NOT NULL, 2
				 FROM outbox
				 WHERE NOT l.wrapped AND kind = l.kind AND failed_at IS NULL
				 ORDER BY kind, tenant_id, next_at, id
				 LIMIT 1)
			) candidate
			ORDER BY candidate.branch
			LIMIT 1`

	// claimLaneHead — задача, которую полоса отдаёт: у обычного вида — с ближайшим сроком,
	// у упорядоченного — старейшая по id, включая аренды и повторы. Ветки взаимоисключающие.
	claimLaneHead = `
			SELECT n.next_at, n.id WHERE NOT (l.kind = ANY($9::text[]))
			UNION ALL
			(SELECT next_at, id
			 FROM outbox
			 WHERE l.kind = ANY($9::text[]) AND kind IN ` + orderedLaneKinds + `
				AND kind = l.kind AND tenant_id = n.tenant_id AND failed_at IS NULL
			 ORDER BY id
			 LIMIT 1)`

	// claimLanes обходит полосы каждого вида по кругу от курсора. Строка с visited = 0 — начало
	// обхода, не полоса. Обход вида кончается на $2 готовых полосах, на $10 посещённых или
	// по возвращении к курсору.
	claimLanes = `
		claim_lanes(kind, position, wrapped, visited, ready, yields, head_next_at, head_id) AS (
			SELECT kind, $8::text, false, 0, 0, false, NULL::timestamptz, NULL::bigint
			FROM claim_kinds
			WHERE NOT (kind = ANY($6::text[]))
			UNION ALL
			SELECT l.kind, n.tenant_id, n.wrapped, l.visited + 1,
				l.ready + (yield.ok)::integer, yield.ok, head.next_at, head.id
			FROM claim_lanes l
			CROSS JOIN LATERAL (` + claimNextLane + `
			) n
			CROSS JOIN LATERAL (` + claimLaneHead + `
			) head
			CROSS JOIN LATERAL (
				SELECT head.next_at <= $1 AND COALESCE(($7::jsonb ->> n.tenant_id)::integer, $3) > 0
					AND (NOT (n.tenant_id = ANY($12::text[])) OR NOT EXISTS (
						SELECT 1 FROM claim_recent r WHERE r.kind = l.kind
					)) AS ok
			) yield
			WHERE l.visited < $10 AND l.ready < $2 AND NOT (n.wrapped AND ($8::text IS NULL OR n.tenant_id > $8))
		)`

	// claimOldest — старейшая созревшая задача каждого вида по outbox_kind_next_idx: один спуск
	// по индексу. Её магазин берётся первым, даже если круг до него ещё не дошёл: так задержка
	// созревшей задачи не зависит от числа отложенных полос перед ней.
	claimOldest = `
		claim_oldest AS MATERIALIZED (
			SELECT k.kind, oldest.tenant_id, head.next_at AS head_next_at, head.id AS head_id
			FROM claim_kinds k
			CROSS JOIN LATERAL (
				SELECT tenant_id, next_at, id
				FROM outbox
				WHERE kind = k.kind AND failed_at IS NULL AND next_at <= $1
				ORDER BY next_at, id
				LIMIT 1
			) oldest
			CROSS JOIN LATERAL (
				SELECT oldest.next_at, oldest.id WHERE NOT (k.kind = ANY($9::text[]))
				UNION ALL
				(SELECT next_at, id
				 FROM outbox
				 WHERE k.kind = ANY($9::text[]) AND kind IN ` + orderedLaneKinds + `
					AND kind = k.kind AND tenant_id = oldest.tenant_id AND failed_at IS NULL
				 ORDER BY id
				 LIMIT 1)
			) head
			WHERE NOT (k.kind = ANY($6::text[])) AND head.next_at <= $1
				AND COALESCE(($7::jsonb ->> oldest.tenant_id)::integer, $3) > 0
				AND (NOT (oldest.tenant_id = ANY($12::text[])) OR NOT EXISTS (
					SELECT 1 FROM claim_recent r WHERE r.kind = k.kind
				))
		)`

	// claimRecent даёт независимого кандидата, когда очередь возглавляют молчащие магазины.
	// Обратный проход по индексу ограничен; если в выборке нет допуска, остаётся обычный круг.
	claimRecent = `
		claim_recent AS MATERIALIZED (
			SELECT k.kind, recent.tenant_id, recent.next_at, recent.id
			FROM claim_kinds k
			CROSS JOIN LATERAL (
				SELECT candidate.tenant_id, candidate.next_at, candidate.id
				FROM (
					SELECT tenant_id, next_at, id
					FROM outbox
					WHERE kind = k.kind AND failed_at IS NULL AND next_at <= $1
					ORDER BY next_at DESC, id DESC
					LIMIT $10
				) candidate
				WHERE COALESCE(($7::jsonb ->> candidate.tenant_id)::integer, $3) > 0
					AND NOT (candidate.tenant_id = ANY($12::text[]))
				ORDER BY candidate.next_at DESC, candidate.id DESC
				LIMIT 1
			) recent
			WHERE $11::boolean AND NOT (k.kind = ANY($6::text[]) OR k.kind = ANY($9::text[]))
		)`

	// claimChosen — до $2 магазинов с готовой полосой: при необходимости один из конца очереди,
	// затем магазины старейших задач видов и остальные по кругу от курсора.
	claimChosen = `
		claim_ready AS MATERIALIZED (
			SELECT DISTINCT ON (kind, tenant_id) kind, tenant_id, wrapped, head_next_at, head_id, ordered, priority
			FROM (
				SELECT kind, position AS tenant_id, wrapped, head_next_at, head_id, 1 AS priority
				FROM claim_lanes
				WHERE visited > 0 AND yields
				UNION ALL
				SELECT kind, tenant_id, $8::text IS NOT NULL AND tenant_id <= $8, head_next_at, head_id, 0
				FROM claim_oldest
				UNION ALL
				SELECT kind, tenant_id, $8::text IS NOT NULL AND tenant_id <= $8, next_at, id, -1
				FROM claim_recent
			) lane
			CROSS JOIN LATERAL (SELECT lane.kind = ANY($9::text[]) AS ordered) o
			ORDER BY kind, tenant_id, priority
		), claim_chosen AS MATERIALIZED (
			SELECT tenant_id, wrapped, priority
			FROM (
				SELECT DISTINCT ON (tenant_id) tenant_id, wrapped, priority
				FROM claim_ready
				ORDER BY tenant_id, priority
			) tenants
			ORDER BY priority, wrapped, tenant_id
			LIMIT $2
		)`

	// claimCandidates — задачи выбранных магазинов, не больше квоты: каждый выбранный магазин даёт
	// задачу в первом круге, поэтому больше $2 − магазинов + 1 задач магазина в пачку не попадёт.
	claimCandidates = `
		claim_candidates AS MATERIALIZED (
			SELECT picked.id, picked.next_at, picked.position, c.wrapped, c.tenant_id
			FROM claim_chosen c
			CROSS JOIN LATERAL (
				SELECT least(
					COALESCE(($7::jsonb ->> c.tenant_id)::integer, $3),
					$2 - (SELECT count(*) FROM claim_chosen) + 1
				) AS quota
			) q
			CROSS JOIN LATERAL (
				SELECT task.id, task.next_at, row_number() OVER (ORDER BY task.next_at, task.id) AS position
				FROM claim_ready r
				CROSS JOIN LATERAL (
					SELECT r.head_id AS id, r.head_next_at AS next_at WHERE r.ordered
					UNION ALL
					(SELECT id, next_at
					 FROM outbox
					 WHERE NOT r.ordered AND kind = r.kind AND tenant_id = r.tenant_id
						AND failed_at IS NULL AND next_at <= $1
					 ORDER BY next_at, id
					 LIMIT q.quota)
				) task
				WHERE r.tenant_id = c.tenant_id
				ORDER BY task.next_at, task.id
				LIMIT q.quota
			) picked
			ORDER BY picked.position, c.wrapped, c.tenant_id, picked.next_at, picked.id
			LIMIT $2
		)`

	// claimLocked берёт выбранные строки по первичному ключу, пропуская занятые.
	claimLocked = `
		claim_locked AS (
			SELECT selected.id
			FROM claim_candidates c
			CROSS JOIN LATERAL (
				SELECT o.id, o.failed_at, o.next_at
				FROM outbox o
				WHERE o.id = c.id
				-- Не переносить проверку готовности внутрь поиска: иначе неверная оценка выбирает другой индекс.
				OFFSET 0
				FOR UPDATE SKIP LOCKED
			) selected
			WHERE selected.failed_at IS NULL AND selected.next_at <= $1
		), claim_updated AS (
			UPDATE outbox
			SET attempts = outbox.attempts + 1,
				next_at = $4
			FROM claim_locked
			WHERE outbox.id = claim_locked.id
			RETURNING outbox.id, outbox.kind, outbox.tenant_id, outbox.payload, outbox.attempts, outbox.created_at,
				COALESCE(outbox.unknown_failures, 0) AS unknown_failures
		)`

	// claimCursor — где продолжить круг: самая ранняя по кругу из последних посещённых полос
	// видов и, при полной пачке, последний выбранный обходом магазин (не магазин старейшей задачи).
	// Так ни одна полоса не перескакивается, а курсор двигается и при пустом взятии.
	claimCursor = `
		claim_cursor AS (
			SELECT COALESCE((
				SELECT edge.tenant_id
				FROM (
					(SELECT DISTINCT ON (kind) position AS tenant_id, wrapped
					 FROM claim_lanes
					 WHERE visited > 0
					 ORDER BY kind, visited DESC)
					UNION ALL
					(SELECT tenant_id, wrapped
					 FROM claim_chosen
					 WHERE priority = 1 AND (SELECT count(*) FROM claim_chosen) = $2
					 ORDER BY wrapped DESC, tenant_id DESC
					 LIMIT 1)
				) edge
				ORDER BY edge.wrapped, edge.tenant_id
				LIMIT 1
			), $8::text) AS next
		)`

	claimTasks = `WITH RECURSIVE` + claimQueuedKinds + `,` +
		claimKinds + `,` + claimRecent + `,` + claimLanes + `,` + claimOldest +
		`,` + claimChosen + `,` + claimCandidates + `,` + claimLocked + `,` + claimCursor + `
		SELECT cur.next, r.id, r.kind, r.tenant_id, r.payload, r.attempts, r.created_at, r.unknown_failures
		FROM claim_cursor cur
		LEFT JOIN (
			SELECT u.*, c.position, c.wrapped, c.next_at, c.tenant_id AS circle_tenant
			FROM claim_updated u
			JOIN claim_candidates c ON c.id = u.id
		) r ON true
		ORDER BY r.position, r.wrapped, r.circle_tenant, r.next_at, r.id`
)

// ErrUnorderedLane — упорядоченный вид без частичного индекса головы полосы.
var ErrUnorderedLane = errs.New(
	errs.Internal,
	"outbox.unordered_lane",
	"упорядоченный вид outbox не входит в индекс головы полосы",
)

// OrderedTaskKinds — виды, для которых индекс головы полосы построен (миграция 00070). Новый
// упорядоченный вид добавляется сюда, в orderedLaneKinds и в индекс отдельной миграцией.
var OrderedTaskKinds = []string{
	"participant.settings.sync",
	"moderation.participant.status",
	"reference.changed",
	"search-synonyms.changed",
}

type claimedRow struct {
	next            *string
	id              *int64
	kind, tenant    *string
	payload         []byte
	attempts        *int
	createdAt       *time.Time
	unknownFailures *int
}

// Claim берёт созревшие задачи: не больше PerTenant на магазин, всего не больше Limit, занятые
// другим процессом пропускаются. Взятие продлевает срок до конца аренды и считает попытку.
// Next — курсор круга для следующего взятия, в том числе после пустого.
func (q *TaskQueue) Claim(ctx context.Context, request ports.ClaimRequest) (ports.ClaimResult, error) {
	for _, kind := range request.OrderedKinds {
		if !slices.Contains(OrderedTaskKinds, kind) {
			return ports.ClaimResult{}, ErrUnorderedLane.WithMessage("вид " + kind)
		}
	}
	var result ports.ClaimResult
	err := q.source.withConnection(ctx, func(ctx context.Context, db *gorm.DB) error {
		var err error
		result, err = claimOutboxTasks(ctx, db, request)
		return err
	})
	return result, err
}

func claimOutboxTasks(ctx context.Context, db *gorm.DB, request ports.ClaimRequest) (ports.ClaimResult, error) {
	slots, err := json.Marshal(request.TenantSlots)
	if err != nil {
		return ports.ClaimResult{}, err
	}
	rows, err := rawSQL(
		ctx,
		db,
		claimTasks,
		request.Now,
		request.Limit,
		request.PerTenant,
		request.LeaseUntil,
		append([]string{}, request.Kinds...),
		append([]string{}, request.ExcludeKinds...),
		string(slots),
		claimAfter(request.After),
		append([]string{}, request.OrderedKinds...),
		ports.ClaimLaneVisits,
		request.PreferRecent,
		append([]ids.TenantID{}, request.DeprioritizedTenants...),
		request.Gate.Key,
		append([]string{}, request.Gate.Kinds...),
	).Rows()
	if err != nil {
		return ports.ClaimResult{}, fmt.Errorf("взятие задач: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	result := ports.ClaimResult{Next: request.After}
	for rows.Next() {
		var row claimedRow
		err := rows.Scan(
			&row.next,
			&row.id,
			&row.kind,
			&row.tenant,
			&row.payload,
			&row.attempts,
			&row.createdAt,
			&row.unknownFailures,
		)
		if err != nil {
			return ports.ClaimResult{}, fmt.Errorf("взятие задач: %w", err)
		}
		result.Next = row.cursor()
		if row.id != nil {
			result.Tasks = append(result.Tasks, row.task())
		}
	}
	if err := rows.Err(); err != nil {
		return ports.ClaimResult{}, fmt.Errorf("взятие задач: %w", err)
	}
	return result, nil
}

// claimAfter — параметр курсора: NULL, пока круг не начат.
func claimAfter(cursor ports.ClaimCursor) any {
	if !cursor.Started {
		return nil
	}
	return string(cursor.Tenant)
}

func (r claimedRow) cursor() ports.ClaimCursor {
	if r.next == nil {
		return ports.ClaimCursor{}
	}
	return ports.ClaimAfter(ids.TenantID(*r.next))
}

func (r claimedRow) task() ports.Task {
	return ports.Task{
		ID:              *r.id,
		Kind:            *r.kind,
		TenantID:        ids.TenantID(*r.tenant),
		Payload:         r.payload,
		Attempts:        *r.attempts,
		CreatedAt:       *r.createdAt,
		UnknownFailures: *r.unknownFailures,
	}
}
