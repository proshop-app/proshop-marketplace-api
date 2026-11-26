-- Фильтр оператора «зависшие»: группы, ждущие оплаты или сверки отмены. Без индекса фильтр читал
-- все группы и шёл по покупкам в порядке ключа. Частичный индекс держит только живые ожидания —
-- его размер не растёт с историей заказов, условие совпадает с формой listStuckCheckouts.
-- +goose NO TRANSACTION
-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS checkout_groups_stuck_idx ON checkout_groups (checkout_id)
    WHERE order_progress->>'ReconciliationRequired' = 'true' OR order_progress->>'Status' = 'awaiting_payment';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS checkout_groups_stuck_idx;
