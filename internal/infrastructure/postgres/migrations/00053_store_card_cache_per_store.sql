-- +goose Up
-- Строка остаётся после удаления магазина: повторная вставка не оживляет старый ключ Redis.
CREATE TABLE store_card_cache_versions (
 tenant_id text PRIMARY KEY,
 version bigint NOT NULL DEFAULT 1
);
INSERT INTO store_card_cache_versions (tenant_id) SELECT tenant_id FROM store_cards;
-- Старые реплики при отсутствии поколения читают источник, а не устаревший Redis.
DROP TABLE store_card_cache_generation;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION invalidate_store_card_cache() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'UPDATE' AND OLD.public = NEW.public AND OLD.page_public = NEW.page_public
  AND OLD.cache_signature = NEW.cache_signature
  AND (OLD.card - ARRAY['offers', 'fromPrice', 'rating', 'products'])
   IS NOT DISTINCT FROM (NEW.card - ARRAY['offers', 'fromPrice', 'rating', 'products'])
  AND OLD.header->'categories' IS NOT DISTINCT FROM NEW.header->'categories' THEN
  RETURN NULL;
 END IF;
 INSERT INTO store_card_cache_versions (tenant_id, version)
 VALUES (CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END, 1)
 ON CONFLICT (tenant_id) DO UPDATE SET version = store_card_cache_versions.version + 1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
CREATE TABLE store_card_cache_generation (
 id integer PRIMARY KEY CHECK (id = 1),
 version bigint NOT NULL DEFAULT 1
);
INSERT INTO store_card_cache_generation (id, version)
 SELECT 1, COALESCE(sum(version), 0) + 1 FROM store_card_cache_versions;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION invalidate_store_card_cache() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'UPDATE' AND OLD.public = NEW.public AND OLD.page_public = NEW.page_public
  AND OLD.cache_signature = NEW.cache_signature
  AND (OLD.card - ARRAY['offers', 'fromPrice', 'rating', 'products'])
   IS NOT DISTINCT FROM (NEW.card - ARRAY['offers', 'fromPrice', 'rating', 'products'])
  AND OLD.header->'categories' IS NOT DISTINCT FROM NEW.header->'categories' THEN
  RETURN NULL;
 END IF;
 UPDATE store_card_cache_generation SET version = version + 1 WHERE id = 1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
DROP TABLE store_card_cache_versions;
