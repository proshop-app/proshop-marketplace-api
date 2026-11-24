-- Взятие задач outbox прыжками по полосам «вид × магазин»: стоимость зависит от пачки и потолка
-- посещённых полос, а не от длины очереди. Выполненная задача удаляется при завершении: done_at и
-- чистка завершённых полным обходом больше не нужны. Порядок выката — docs/outbox.md.
-- +goose NO TRANSACTION
-- +goose Up
-- Столбец done_at снимает следующая миграция 00071: здесь он ещё есть, повтор безопасен.
-- Временный индекс завершённых: удаление пачками и хвост под блокировкой не обходят таблицу.
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_done_cleanup_idx ON outbox (id) WHERE done_at IS NOT NULL;
-- +goose StatementBegin
DO $$
DECLARE
    removed bigint;
BEGIN
    LOOP
        DELETE FROM outbox WHERE id IN (SELECT id FROM outbox WHERE done_at IS NOT NULL LIMIT 10000);
        GET DIAGNOSTICS removed = ROW_COUNT;
        COMMIT;
        EXIT WHEN removed = 0;
    END LOOP;
END $$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_lane_due_idx
    ON outbox (kind, tenant_id, next_at, id) WHERE failed_at IS NULL;
-- Старейшая созревшая задача вида — одна проба на взятие, без обхода полос.
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_kind_next_idx
    ON outbox (kind, next_at, id) WHERE failed_at IS NULL;
-- Голова упорядоченной полосы — старейшая по id; список видов совпадает с postgres.OrderedTaskKinds.
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_ordered_head_idx
    ON outbox (kind, tenant_id, id)
    WHERE failed_at IS NULL AND kind IN ('participant.settings.sync', 'moderation.participant.status',
        'reference.changed', 'search-synonyms.changed');
-- Невзятая задача склеивает повтор по ключу; взятая уже читает состояние и не склеивается.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS outbox_untaken_dedup_idx
    ON outbox (dedup_key) WHERE attempts = 0;
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_retry_created_idx
    ON outbox (kind, created_at)
    WHERE failed_at IS NULL AND (last_error IS NOT NULL OR unknown_failures IS NOT NULL);
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM unnest(ARRAY[
            'outbox_lane_due_idx',
            'outbox_kind_next_idx',
            'outbox_ordered_head_idx',
            'outbox_untaken_dedup_idx',
            'outbox_retry_created_idx'
        ]) AS expected(name)
        LEFT JOIN pg_index i ON i.indexrelid = to_regclass(expected.name)
        WHERE i.indisvalid IS DISTINCT FROM true
    ) THEN
        RAISE EXCEPTION 'новые индексы outbox не valid; повторите штатную миграцию';
    END IF;
END $$;
-- +goose StatementEnd

-- Завершённое старыми процессами за время сборки индексов — вторым проходом пачками, без блокировки.
-- +goose StatementBegin
DO $$
DECLARE
    removed bigint;
BEGIN
    LOOP
        DELETE FROM outbox WHERE id IN (SELECT id FROM outbox WHERE done_at IS NOT NULL LIMIT 10000);
        GET DIAGNOSTICS removed = ROW_COUNT;
        COMMIT;
        EXIT WHEN removed = 0;
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS outbox_retry_created_idx;
DROP INDEX CONCURRENTLY IF EXISTS outbox_untaken_dedup_idx;
DROP INDEX CONCURRENTLY IF EXISTS outbox_ordered_head_idx;
DROP INDEX CONCURRENTLY IF EXISTS outbox_kind_next_idx;
DROP INDEX CONCURRENTLY IF EXISTS outbox_lane_due_idx;
DROP INDEX CONCURRENTLY IF EXISTS outbox_done_cleanup_idx;
