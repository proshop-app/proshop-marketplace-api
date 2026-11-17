-- +goose Up
-- DROP COLUMN меняет только метаданные, не переписывает строки. Не ждём
-- ACCESS EXCLUSIVE за работающими транзакциями и не создаём очередь писателей:
-- при занятой таблице миграция сразу откатывается, оператор повторяет её позже.
LOCK TABLE participants IN ACCESS EXCLUSIVE MODE NOWAIT;
ALTER TABLE participants
    DROP COLUMN IF EXISTS subsidy,
    DROP COLUMN IF EXISTS free_delivery_from;

-- +goose Down
-- Удалённые значения невосстановимы; nullable-колонки нужны для отката схемы.
LOCK TABLE participants IN ACCESS EXCLUSIVE MODE NOWAIT;
ALTER TABLE participants
    ADD COLUMN IF NOT EXISTS subsidy jsonb,
    ADD COLUMN IF NOT EXISTS free_delivery_from bigint CHECK (free_delivery_from >= 0);
