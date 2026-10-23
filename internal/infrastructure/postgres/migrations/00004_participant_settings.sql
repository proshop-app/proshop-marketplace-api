-- Настройки магазина с сервера: согласие на участие и субсидия доставки.
-- Видимость предложений теперь требует согласия магазина.

-- +goose Up
ALTER TABLE participants
    ADD COLUMN shop_enabled       boolean NOT NULL DEFAULT false,
    ADD COLUMN subsidy            jsonb,
    ADD COLUMN settings_synced_at timestamptz;

-- Прежние документы индекса показывают участников без учёта согласия
-- магазина: версии участников растут, их предложения переиндексируются.
-- Задачи созревают через 15 минут — после выката, иначе под прежней версии
-- успел бы записать документ по старому правилу с той же версией.
UPDATE participants SET version = version + 1, updated_at = now();

INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
SELECT 'catalog.tenant.sync', tenant_id, 'catalog.tenant.sync:' || tenant_id,
       jsonb_build_object('tenantId', tenant_id), now() + interval '15 minutes', now()
FROM participants
ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO NOTHING;

-- +goose Down
ALTER TABLE participants
    DROP COLUMN settings_synced_at,
    DROP COLUMN subsidy,
    DROP COLUMN shop_enabled;
