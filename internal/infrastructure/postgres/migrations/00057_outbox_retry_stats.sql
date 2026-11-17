-- +goose NO TRANSACTION
-- +goose Up
-- Индекс содержит только уже падавшие задачи, а не всю очередь или первые аренды.
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_retry_kind_created_idx
    ON outbox (kind, created_at)
    WHERE done_at IS NULL AND failed_at IS NULL
        AND (last_error IS NOT NULL OR unknown_failures IS NOT NULL);
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_index
        WHERE indexrelid = to_regclass('outbox_retry_kind_created_idx') AND indisvalid) THEN
        RAISE EXCEPTION 'индекс повторов не valid; повторите штатную миграцию';
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS outbox_retry_kind_created_idx;
