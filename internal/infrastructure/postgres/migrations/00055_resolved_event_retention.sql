-- +goose NO TRANSACTION
-- +goose Up
-- Незавершённые события не участвуют в очистке независимо от возраста.
CREATE INDEX CONCURRENTLY IF NOT EXISTS parked_events_resolved_updated_idx
    ON parked_events (updated_at, id) WHERE status = 'resolved';

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM unnest(ARRAY[
            'parked_events_resolved_updated_idx'
        ]) AS expected(name)
        LEFT JOIN pg_index i ON i.indexrelid = to_regclass(expected.name)
        WHERE i.indisvalid IS DISTINCT FROM true
    ) THEN
        RAISE EXCEPTION 'новые индексы не valid; старые индексы сохранены, повторите штатную миграцию';
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS parked_events_resolved_updated_idx;
