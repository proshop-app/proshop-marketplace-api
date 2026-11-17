-- +goose NO TRANSACTION
-- +goose Up
-- Nullable-колонки не переписывают таблицу. Проверка старых строк вынесена из ADD.
ALTER TABLE outbox
    ADD COLUMN IF NOT EXISTS unknown_failures integer,
    ADD COLUMN IF NOT EXISTS failed_at timestamptz,
    ADD COLUMN IF NOT EXISTS failure_reason text;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'outbox'::regclass
        AND conname = 'outbox_failure_state') THEN
        ALTER TABLE outbox ADD CONSTRAINT outbox_failure_state
            CHECK ((failed_at IS NULL) = (failure_reason IS NULL)) NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'outbox'::regclass
        AND conname = 'outbox_failure_not_done') THEN
        ALTER TABLE outbox ADD CONSTRAINT outbox_failure_not_done
            CHECK (failed_at IS NULL OR done_at IS NULL) NOT VALID;
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE outbox VALIDATE CONSTRAINT outbox_failure_state;
ALTER TABLE outbox VALIDATE CONSTRAINT outbox_failure_not_done;

-- Старые индексы обслуживают очередь до полного построения новых.
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_pending_due_idx
    ON outbox (next_at, id) WHERE done_at IS NULL AND failed_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_pending_tenant_due_idx
    ON outbox (tenant_id, next_at, id) WHERE done_at IS NULL AND failed_at IS NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_quarantine_id_idx
    ON outbox (id) WHERE failed_at IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_quarantine_kind_id_idx
    ON outbox (kind, id) WHERE failed_at IS NOT NULL;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM unnest(ARRAY[
            'outbox_pending_due_idx',
            'outbox_pending_tenant_due_idx',
            'outbox_quarantine_id_idx',
            'outbox_quarantine_kind_id_idx'
        ]) AS expected(name)
        LEFT JOIN pg_index i ON i.indexrelid = to_regclass(expected.name)
        WHERE i.indisvalid IS DISTINCT FROM true
    ) THEN
        RAISE EXCEPTION 'новые индексы не valid; старые индексы сохранены, повторите штатную миграцию';
    END IF;
END $$;
-- +goose StatementEnd
DROP INDEX CONCURRENTLY IF EXISTS outbox_due_idx;
DROP INDEX CONCURRENTLY IF EXISTS outbox_tenant_due_idx;

-- +goose Down
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_due_idx ON outbox (done_at, next_at);
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_tenant_due_idx
    ON outbox (tenant_id, next_at, id) WHERE done_at IS NULL;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM unnest(ARRAY[
            'outbox_due_idx',
            'outbox_tenant_due_idx'
        ]) AS expected(name)
        LEFT JOIN pg_index i ON i.indexrelid = to_regclass(expected.name)
        WHERE i.indisvalid IS DISTINCT FROM true
    ) THEN
        RAISE EXCEPTION 'новые индексы не valid; старые индексы сохранены, повторите штатную миграцию';
    END IF;
END $$;
-- +goose StatementEnd
DROP INDEX CONCURRENTLY IF EXISTS outbox_quarantine_kind_id_idx;
DROP INDEX CONCURRENTLY IF EXISTS outbox_quarantine_id_idx;
DROP INDEX CONCURRENTLY IF EXISTS outbox_pending_tenant_due_idx;
DROP INDEX CONCURRENTLY IF EXISTS outbox_pending_due_idx;
ALTER TABLE outbox
    DROP CONSTRAINT IF EXISTS outbox_failure_not_done,
    DROP CONSTRAINT IF EXISTS outbox_failure_state,
    DROP COLUMN IF EXISTS failure_reason,
    DROP COLUMN IF EXISTS failed_at,
    DROP COLUMN IF EXISTS unknown_failures;
