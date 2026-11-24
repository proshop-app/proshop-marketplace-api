-- Засев очереди outbox для замеров взятия задач на одноразовой базе после миграций.
-- Только нейтральные магазины t0001…t1000, никаких данных конкретных магазинов.
--
--   psql "$URL" -v scale=1 -f scripts/bench/outbox-seed.sql      -- 10 млн незавершённых задач
--   psql "$URL" -v scale=0.01 -f scripts/bench/outbox-seed.sql   -- 100 тысяч
--
-- Распределение: пять магазинов волны по 10% очереди, 95 крупных — 38% очереди, 900 мелких — хвост
-- оставшихся 12% видов индексации и статусов модерации; у каждого магазина — упорядоченная задача
-- настроек и карточка; задачи оформления — у сотни магазинов. 10% задач взяты в аренду (срок впереди),
-- 1% — в карантине, ещё 20% объёма — завершённые задачи, которые ждут чистки (только для схемы без
-- удаления при завершении: колонка done_at).
\set ON_ERROR_STOP on
\if :{?scale}
\else
\set scale 1
\endif

SET synchronous_commit = off;

CREATE TEMP TABLE bench_shape AS
SELECT * FROM (VALUES
    -- магазины first..last, задач на магазин при scale=1, вид
    (1, 5, 1000000, 'catalog.offer.sync'),
    (6, 100, 36000, 'catalog.offer.sync'),
    (6, 100, 4000, 'moderation.offer.status'),
    (101, 1000, 1000, 'catalog.offer.sync'),
    (101, 1000, 330, 'moderation.offer.status')
) AS s(first_tenant, last_tenant, per_tenant, kind);

INSERT INTO outbox (kind, tenant_id, dedup_key, payload, attempts, next_at, created_at)
SELECT s.kind,
    'b' || lpad(t::text, 4, '0'),
    s.kind || ':b' || lpad(t::text, 4, '0') || ':p' || n,
    jsonb_build_object('tenantId', 'b' || lpad(t::text, 4, '0'), 'productId', 'p' || n),
    CASE WHEN n % 10 = 0 THEN 1 ELSE 0 END,
    -- Аренда — срок в будущем; остальные созрели в прошлом с разбросом по возрасту.
    CASE WHEN n % 10 = 0 THEN now() + interval '90 seconds'
        ELSE now() - make_interval(secs => (n % 3600)::int) END,
    now() - make_interval(secs => (n % 3600)::int + 1)
FROM bench_shape s
CROSS JOIN LATERAL generate_series(s.first_tenant, s.last_tenant) t
CROSS JOIN LATERAL generate_series(1, greatest(1, (s.per_tenant * :scale)::bigint)) n;

-- Упорядоченные и точечные виды: по одной задаче настроек и карточки на магазин.
INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
SELECT k.kind, 'b' || lpad(t::text, 4, '0'), k.kind || ':b' || lpad(t::text, 4, '0'),
    jsonb_build_object('tenantId', 'b' || lpad(t::text, 4, '0')), now() - interval '1 minute', now() - interval '2 minutes'
FROM generate_series(1, 1000) t
CROSS JOIN (VALUES ('participant.settings.sync'), ('participant.store-card.refresh')) AS k(kind);

-- Оформление: сотня магазинов по три задачи каждого вида.
INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
SELECT k.kind, 'b' || lpad(t::text, 4, '0'), k.kind || ':b' || lpad(t::text, 4, '0') || ':' || n,
    jsonb_build_object('checkoutId', 'c' || t || '-' || n), now() - interval '1 second', now() - interval '2 seconds'
FROM generate_series(1, 1000, 10) t
CROSS JOIN generate_series(1, 3) n
CROSS JOIN (VALUES ('checkout.create_order'), ('checkout.poll_order'), ('checkout.cancel_order'),
    ('checkout.expire_creation')) AS k(kind);

-- Карантин: 1% задач индексации.
UPDATE outbox SET failed_at = now() - interval '1 hour', failure_reason = 'bench.invalid'
WHERE kind = 'catalog.offer.sync' AND id % 100 = 7;

\if :{?done}
-- Завершённые задачи старой схемы: им нужна колонка done_at.
INSERT INTO outbox (kind, tenant_id, payload, attempts, next_at, created_at, done_at)
SELECT 'catalog.offer.sync', 'b' || lpad((1 + n % 1000)::text, 4, '0'), '{}', 1,
    now() - interval '1 day', now() - interval '1 day', now() - make_interval(hours => (n % 96)::int)
FROM generate_series(1, (2000000 * :scale)::bigint) n;
\endif

VACUUM ANALYZE outbox;
SELECT count(*) FILTER (WHERE failed_at IS NULL) AS pending, count(*) FILTER (WHERE failed_at IS NOT NULL) AS failed,
    count(DISTINCT tenant_id) AS tenants
FROM outbox;
