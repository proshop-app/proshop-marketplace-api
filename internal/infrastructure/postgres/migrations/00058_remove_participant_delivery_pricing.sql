-- +goose Up
-- Обе таблицы блокируются до первого DROP; отказ NOWAIT откатывает всю миграцию.
-- Только метаданные, без переписывания строк и ожидания текущих писателей.
LOCK TABLE participants, checkout_groups IN ACCESS EXCLUSIVE MODE NOWAIT;
ALTER TABLE participants
    DROP COLUMN IF EXISTS subsidy,
    DROP COLUMN IF EXISTS free_delivery_from;
ALTER TABLE checkout_groups
    DROP COLUMN IF EXISTS subsidy,
    DROP COLUMN IF EXISTS buyer_delivery;

-- +goose Down
-- Прежний код читает суммы в int64: DEFAULT 0 NOT NULL, без NULL после отката.
LOCK TABLE participants, checkout_groups IN ACCESS EXCLUSIVE MODE NOWAIT;
ALTER TABLE participants
    ADD COLUMN IF NOT EXISTS subsidy jsonb,
    ADD COLUMN IF NOT EXISTS free_delivery_from bigint NOT NULL DEFAULT 0 CHECK (free_delivery_from >= 0);
ALTER TABLE checkout_groups
    ADD COLUMN IF NOT EXISTS subsidy bigint NOT NULL DEFAULT 0 CHECK (subsidy >= 0),
    ADD COLUMN IF NOT EXISTS buyer_delivery bigint NOT NULL DEFAULT 0 CHECK (buyer_delivery >= 0);
