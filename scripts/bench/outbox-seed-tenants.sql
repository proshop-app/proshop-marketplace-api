-- Засев для замера взятия при многих магазинах: 10 тысяч магазинов, по задаче двух точечных видов
-- и по задаче упорядоченного вида настроек. Режим (-v mode=...):
--   ready    — всё созрело;
--   retry    — всё в повторе (срок через 10 минут): отказ поиска или серверов магазинов;
--   mixed    — всё в повторе, кроме 20 магазинов в середине круга.
--   psql "$URL" -v mode=mixed -f scripts/bench/outbox-seed-tenants.sql
\set ON_ERROR_STOP on
SET synchronous_commit = off;

INSERT INTO outbox (kind, tenant_id, dedup_key, payload, attempts, next_at, created_at)
SELECT k.kind, 'm' || lpad(t::text, 5, '0'), k.kind || ':m' || lpad(t::text, 5, '0'),
    jsonb_build_object('tenantId', 'm' || lpad(t::text, 5, '0')),
    CASE WHEN :'mode' = 'ready' OR (:'mode' = 'mixed' AND t BETWEEN 5001 AND 5020) THEN 0 ELSE 1 END,
    CASE WHEN :'mode' = 'ready' OR (:'mode' = 'mixed' AND t BETWEEN 5001 AND 5020)
        THEN now() - interval '1 minute' ELSE now() + interval '10 minutes' END,
    now() - interval '2 minutes'
FROM generate_series(1, 10000) t
CROSS JOIN (VALUES ('participant.store-card.refresh'), ('catalog.tenant.sync'), ('participant.settings.sync'))
    AS k(kind);

VACUUM ANALYZE outbox;
SELECT count(*) FILTER (WHERE next_at <= now()) AS ready, count(*) AS total, count(DISTINCT tenant_id) AS tenants
FROM outbox;
