-- +goose Up
-- DROP COLUMN меняет только метаданные, не переписывает строки. Не ждём
-- ACCESS EXCLUSIVE за работающими транзакциями и не создаём очередь писателей:
-- при занятой таблице миграция сразу откатывается, оператор повторяет её позже.
LOCK TABLE participants IN ACCESS EXCLUSIVE MODE NOWAIT;
ALTER TABLE participants
    DROP COLUMN IF EXISTS subsidy,
    DROP COLUMN IF EXISTS free_delivery_from;

-- +goose Down
-- Удалённые значения невосстановимы; DEFAULT 0 NOT NULL совместим с прежним чтением в int64.
LOCK TABLE participants IN ACCESS EXCLUSIVE MODE NOWAIT;
ALTER TABLE participants
    ADD COLUMN IF NOT EXISTS subsidy jsonb,
    ADD COLUMN IF NOT EXISTS free_delivery_from bigint NOT NULL DEFAULT 0 CHECK (free_delivery_from >= 0);
