-- Служебные решения и аудит не изменяют исходную идентичность Kafka.
-- +goose Up
ALTER TABLE parked_events
    ADD COLUMN status text NOT NULL DEFAULT 'parked' CHECK (status IN ('parked', 'queued', 'resolved')),
    ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    ADD COLUMN resolution text NOT NULL DEFAULT '',
    ADD COLUMN operator_id text NOT NULL DEFAULT '',
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN published_at timestamptz NOT NULL DEFAULT 'epoch';
CREATE INDEX parked_events_status_id_idx ON parked_events (status, id DESC);
CREATE TABLE parked_event_decisions (
    event_id bigint NOT NULL REFERENCES parked_events(id) ON DELETE CASCADE,
    version bigint NOT NULL,
    status text NOT NULL,
    note text NOT NULL,
    operator_id text NOT NULL,
    decided_at timestamptz NOT NULL,
    PRIMARY KEY (event_id, version)
);
-- Истинные коды региона нельзя вывести из числовых зон перевозчика.
-- Старые настройки сохраняются для аудита, но перестают разрешать участие.
ALTER TABLE participants ADD COLUMN legacy_delivery_settings jsonb;
-- Отчёт одновременно хранит снимок для безопасного отката и перечень для оператора.
CREATE TABLE operator_delivery_migration_report (
    tenant_id text PRIMARY KEY,
    previous_delivery jsonb NOT NULL,
    previous_shop_enabled boolean NOT NULL,
    previous_status text NOT NULL,
    migrated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO operator_delivery_migration_report (tenant_id, previous_delivery, previous_shop_enabled, previous_status)
SELECT tenant_id, delivery_settings, shop_enabled, status
FROM participants
WHERE EXISTS (SELECT 1 FROM jsonb_array_elements_text(delivery_settings->'regions') r WHERE r LIKE '%:%');
UPDATE participants
SET legacy_delivery_settings = delivery_settings,
    delivery_settings = '{"regions":[],"carriers":[]}',
    shop_enabled = false,
    status = 'onboarding',
    version = version + 1,
    updated_at = now()
WHERE tenant_id IN (SELECT tenant_id FROM operator_delivery_migration_report);
INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
SELECT 'catalog.tenant.sync', tenant_id, 'catalog.tenant.sync:' || tenant_id,
       jsonb_build_object('tenantId', tenant_id), now(), now()
FROM participants WHERE legacy_delivery_settings IS NOT NULL
ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO NOTHING;
-- Числовой код ПВЗ не является ISO. Обновление справочника заполнит его через явное соответствие.
UPDATE pickup_points SET region_code = '' WHERE region_code <> '' AND region_code NOT LIKE 'RU-%';
-- +goose Down
DROP TABLE parked_event_decisions;
-- Восстанавливаем архив до его удаления; версии продолжают расти для переиндексации.
UPDATE participants p
SET delivery_settings = r.previous_delivery,
    shop_enabled = r.previous_shop_enabled,
    status = r.previous_status,
    version = p.version + 1,
    updated_at = now()
FROM operator_delivery_migration_report r
WHERE p.tenant_id = r.tenant_id;
INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
SELECT 'catalog.tenant.sync', tenant_id, 'catalog.tenant.sync:' || tenant_id,
       jsonb_build_object('tenantId', tenant_id), now(), now()
FROM operator_delivery_migration_report
ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO NOTHING;
ALTER TABLE participants DROP COLUMN legacy_delivery_settings;
DROP TABLE operator_delivery_migration_report;
DROP INDEX parked_events_status_id_idx;
ALTER TABLE parked_events
    DROP COLUMN status,
    DROP COLUMN version,
    DROP COLUMN resolution,
    DROP COLUMN operator_id,
    DROP COLUMN updated_at,
    DROP COLUMN published_at;
