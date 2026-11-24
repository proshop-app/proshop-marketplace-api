-- Взятие задач outbox прыжками по полосам «вид × магазин»: стоимость зависит от пачки и числа
-- магазинов, а не от длины очереди. Выполненная задача удаляется при завершении: done_at и
-- чистка завершённых полным обходом больше не нужны. Порядок выката — docs/outbox.md.
-- +goose NO TRANSACTION
-- +goose Up
-- Завершённые задачи удаляются пачками с фиксацией каждой: ни долгой транзакции, ни блокировки таблицы.
-- +goose StatementBegin
DO $$
DECLARE
    removed bigint;
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'outbox' AND column_name = 'done_at') THEN
        LOOP
            DELETE FROM outbox WHERE id IN (SELECT id FROM outbox WHERE done_at IS NOT NULL LIMIT 10000);
            GET DIAGNOSTICS removed = ROW_COUNT;
            COMMIT;
            EXIT WHEN removed = 0;
        END LOOP;
    END IF;
END $$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_lane_due_idx
    ON outbox (kind, tenant_id, next_at, id) WHERE failed_at IS NULL;
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

-- Хвост, завершённый старыми процессами за время сборки индексов, удаляется под короткой
-- блокировкой вместе со столбцом: вместе с ним уходят индексы и ограничение на done_at.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'outbox' AND column_name = 'done_at') THEN
        SET LOCAL lock_timeout = '10s';
        LOCK TABLE outbox IN ACCESS EXCLUSIVE MODE;
        DELETE FROM outbox WHERE done_at IS NOT NULL;
        ALTER TABLE outbox DROP COLUMN done_at;
    END IF;
END $$;
-- +goose StatementEnd
DROP INDEX CONCURRENTLY IF EXISTS outbox_kind_due;
DROP INDEX CONCURRENTLY IF EXISTS outbox_pending_due_idx;
DROP INDEX CONCURRENTLY IF EXISTS outbox_pending_tenant_due_idx;
DROP INDEX CONCURRENTLY IF EXISTS outbox_pending_dedup_idx;
DROP INDEX CONCURRENTLY IF EXISTS outbox_retry_kind_created_idx;
-- Очередь живёт удалениями: мёртвые строки головы очереди убираются раньше умолчания в 20% таблицы.
ALTER TABLE outbox SET (autovacuum_vacuum_scale_factor = 0.01, autovacuum_vacuum_insert_scale_factor = 0.05);

-- +goose Down
ALTER TABLE outbox RESET (autovacuum_vacuum_scale_factor, autovacuum_vacuum_insert_scale_factor);
ALTER TABLE outbox ADD COLUMN IF NOT EXISTS done_at timestamptz;
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'outbox'::regclass
        AND conname = 'outbox_failure_not_done') THEN
        ALTER TABLE outbox ADD CONSTRAINT outbox_failure_not_done
            CHECK (failed_at IS NULL OR done_at IS NULL);
    END IF;
END $$;
-- +goose StatementEnd
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS outbox_pending_dedup_idx
    ON outbox (dedup_key) WHERE done_at IS NULL AND attempts = 0;
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_pending_due_idx
    ON outbox (next_at, id) WHERE done_at IS NULL AND failed_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_pending_tenant_due_idx
    ON outbox (tenant_id, next_at, id) WHERE done_at IS NULL AND failed_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_kind_due
    ON outbox (kind, next_at, tenant_id, id) WHERE done_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_retry_kind_created_idx
    ON outbox (kind, created_at)
    WHERE done_at IS NULL AND failed_at IS NULL AND (last_error IS NOT NULL OR unknown_failures IS NOT NULL);
DROP INDEX CONCURRENTLY IF EXISTS outbox_retry_created_idx;
DROP INDEX CONCURRENTLY IF EXISTS outbox_untaken_dedup_idx;
DROP INDEX CONCURRENTLY IF EXISTS outbox_lane_due_idx;
