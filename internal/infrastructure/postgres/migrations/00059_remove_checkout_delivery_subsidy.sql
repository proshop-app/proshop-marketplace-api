-- +goose Up
-- Только метаданные. NOWAIT не ставит миграцию в очередь перед текущими писателями.
LOCK TABLE checkout_groups IN ACCESS EXCLUSIVE MODE NOWAIT;
ALTER TABLE checkout_groups
    DROP COLUMN IF EXISTS subsidy,
    DROP COLUMN IF EXISTS buyer_delivery;

-- +goose Down
-- Прежний код сканирует доступный расчёт в int64: никаких NULL после отката.
LOCK TABLE checkout_groups IN ACCESS EXCLUSIVE MODE NOWAIT;
ALTER TABLE checkout_groups
    ADD COLUMN IF NOT EXISTS subsidy bigint NOT NULL DEFAULT 0 CHECK (subsidy >= 0),
    ADD COLUMN IF NOT EXISTS buyer_delivery bigint NOT NULL DEFAULT 0 CHECK (buyer_delivery >= 0);
