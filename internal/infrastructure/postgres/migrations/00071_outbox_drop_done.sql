-- Снятие done_at после 00070: завершённые уже удалены пачками, под исключительной блокировкой —
-- только хвост последних секунд по временному индексу и изменение схемы. Повтор безопасен.
-- +goose NO TRANSACTION
-- +goose Up
-- Под короткой блокировкой: хвост последних секунд по временному индексу и снятие столбца; вместе
-- со столбцом уходят индексы и ограничение на done_at, включая временный.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'outbox' AND column_name = 'done_at') THEN
        SET LOCAL lock_timeout = '10s';
        LOCK TABLE outbox IN ACCESS EXCLUSIVE MODE;
        EXECUTE 'DELETE FROM outbox WHERE done_at IS NOT NULL';
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
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_done_cleanup_idx ON outbox (id) WHERE done_at IS NOT NULL;
