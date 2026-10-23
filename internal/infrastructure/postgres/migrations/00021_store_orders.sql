-- Независимые заказы и окна оплаты принадлежат магазинам.
-- +goose Up
-- Удаление прежних полей также при обновлении уже установленной недели 2.
ALTER TABLE participants DROP CONSTRAINT IF EXISTS participants_payment_account_status;
ALTER TABLE participants DROP COLUMN IF EXISTS payment_account_id;
ALTER TABLE participants
    ADD COLUMN free_delivery_from bigint NOT NULL DEFAULT 0 CHECK (free_delivery_from >= 0),
    ADD COLUMN delivery_settings jsonb NOT NULL DEFAULT '{}';
-- Старое согласие без обязательных настроек доставки больше не достаточно.
UPDATE participants SET shop_enabled = false, version = version + 1, updated_at = now();
INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
SELECT 'catalog.tenant.sync', tenant_id, 'catalog.tenant.sync:' || tenant_id,
       jsonb_build_object('tenantId', tenant_id), now(), now()
FROM participants
ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO NOTHING;
ALTER TABLE checkouts DROP CONSTRAINT checkouts_status_check;
ALTER TABLE checkouts ADD CONSTRAINT checkouts_status_check CHECK (
    status IN ('draft', 'confirmed', 'creating_orders', 'awaiting_payment', 'partially_paid', 'paid', 'cancelled')
);
ALTER TABLE checkouts ADD COLUMN order_context jsonb NOT NULL DEFAULT '{}';
ALTER TABLE checkout_groups ADD COLUMN order_progress jsonb NOT NULL DEFAULT '{}';
CREATE INDEX checkout_groups_payment_idx ON checkout_groups ((order_progress->>'Status'));
ALTER TABLE pickup_points ADD COLUMN region_code text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE pickup_points DROP COLUMN region_code;
DROP INDEX checkout_groups_payment_idx;
ALTER TABLE checkout_groups DROP COLUMN order_progress;
ALTER TABLE checkouts DROP COLUMN order_context;
ALTER TABLE checkouts DROP CONSTRAINT checkouts_status_check;
ALTER TABLE checkouts ADD CONSTRAINT checkouts_status_check CHECK (status IN ('draft', 'confirmed'));
ALTER TABLE participants DROP COLUMN delivery_settings, DROP COLUMN free_delivery_from;
