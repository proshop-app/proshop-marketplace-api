-- +goose NO TRANSACTION
-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_search_purchase_failed_at_idx
    ON outbox (failed_at, id) WHERE kind = 'search.purchase' AND failed_at IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS outbox_search_purchase_failed_at_idx;
