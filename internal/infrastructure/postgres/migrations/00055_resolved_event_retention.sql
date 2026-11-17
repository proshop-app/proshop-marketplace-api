-- +goose NO TRANSACTION
-- +goose Up
-- Незавершённые события не участвуют в очистке независимо от возраста.
CREATE INDEX CONCURRENTLY IF NOT EXISTS parked_events_resolved_updated_idx
    ON parked_events (updated_at, id) WHERE status = 'resolved';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS parked_events_resolved_updated_idx;
