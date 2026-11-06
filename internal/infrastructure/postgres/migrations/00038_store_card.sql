-- +goose Up
ALTER TABLE participants
 ADD COLUMN store_card jsonb NOT NULL DEFAULT '{}',
 ADD COLUMN card_ready boolean NOT NULL DEFAULT false;
DROP INDEX participants_queue;
CREATE INDEX participants_queue ON participants (created_at, tenant_id)
 WHERE stage = 'candidate' AND awaiting_review AND delivery_valid AND card_ready;
ALTER TABLE catalog_products ADD COLUMN sales_count bigint NOT NULL DEFAULT 0;
CREATE TABLE store_cards (
 tenant_id text PRIMARY KEY REFERENCES participants (tenant_id) ON DELETE CASCADE,
 slug text NOT NULL UNIQUE,
 name text NOT NULL,
 offers integer NOT NULL,
 from_price bigint,
 public boolean NOT NULL,
 profile_version bigint NOT NULL DEFAULT 0,
 summary jsonb NOT NULL,
 header jsonb NOT NULL,
 card jsonb NOT NULL
);
CREATE INDEX store_cards_public ON store_cards (offers DESC, name, tenant_id) WHERE public;
CREATE TABLE store_card_categories (
 category_id text NOT NULL,
 tenant_id text NOT NULL REFERENCES store_cards (tenant_id) ON DELETE CASCADE,
 name text NOT NULL,
 count integer NOT NULL,
 PRIMARY KEY (category_id, tenant_id)
);
-- Пересчёт удаляет категории магазина целиком; отбор по категории покрывает первичный ключ.
CREATE INDEX store_card_categories_tenant ON store_card_categories (tenant_id);
INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
 SELECT 'participant.settings.sync', tenant_id, 'participant.settings.sync:' || tenant_id,
 jsonb_build_object('tenantId', tenant_id), now(), now() FROM participants
 ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO NOTHING;
INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
 SELECT 'participant.store-card.refresh', tenant_id, 'participant.store-card.refresh:' || tenant_id,
 '{}'::jsonb, now(), now() FROM participants
 ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO NOTHING;
ALTER TABLE participant_decisions DROP CONSTRAINT participant_decisions_action_check;
ALTER TABLE participant_decisions ADD CONSTRAINT participant_decisions_action_check
 CHECK (action IN ('candidate', 'return', 'enable', 'disable', 'trust', 'card_hide', 'card_show'));

-- +goose Down
DELETE FROM outbox WHERE kind = 'participant.store-card.refresh' AND done_at IS NULL;
DROP TABLE store_card_categories;
DROP TABLE store_cards;
ALTER TABLE catalog_products DROP COLUMN sales_count;
DROP INDEX participants_queue;
CREATE INDEX participants_queue ON participants (created_at, tenant_id)
 WHERE stage = 'candidate' AND awaiting_review AND delivery_valid;
ALTER TABLE participants DROP COLUMN store_card, DROP COLUMN card_ready;
-- Журнал неизменяем: расширенный словарь действий сохраняется при откате.
