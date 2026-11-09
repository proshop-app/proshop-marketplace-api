-- +goose Up
-- Сохраняем выбранный цвет, а не возвращаем магазины к умолчанию при отказе от двух зон.
WITH appearances AS (
 SELECT tenant_id, jsonb_build_object('color', CASE
  WHEN (store_card #>> '{profile,appearance,color}') ~* '^#[0-9a-f]{6}$'
   THEN upper(store_card #>> '{profile,appearance,color}')
  WHEN (store_card #>> '{profile,appearance,background}') ~* '^#[0-9a-f]{6}$'
   THEN upper(store_card #>> '{profile,appearance,background}')
  ELSE '#125F67' END) AS appearance
 FROM participants
), profiles AS (
 SELECT p.tenant_id,
  jsonb_set(CASE WHEN jsonb_typeof(p.store_card) = 'object' THEN p.store_card ELSE '{}'::jsonb END,
   '{profile}',
   (CASE WHEN jsonb_typeof(p.store_card->'profile') = 'object' THEN p.store_card->'profile' ELSE '{}'::jsonb END)
   || jsonb_build_object('appearance', a.appearance))
  || jsonb_build_object('products', CASE WHEN p.store_card #> '{products,enabled}' = 'false'::jsonb
   THEN '{"enabled":false,"productIds":[]}'::jsonb
   ELSE COALESCE(NULLIF(p.store_card->'products', 'null'::jsonb),
    '{"enabled":true,"productIds":[]}'::jsonb) END) AS cleaned
 FROM participants p JOIN appearances a ON a.tenant_id = p.tenant_id
)
UPDATE participants p
 SET store_card = c.cleaned,
 version = p.version + 1,
 updated_at = now()
 FROM profiles c
 WHERE p.tenant_id = c.tenant_id AND p.store_card IS DISTINCT FROM c.cleaned;

-- Публичные проекции используют тот же выбранный цвет до фонового пересчёта.
UPDATE store_cards s
 SET summary = summary || jsonb_build_object(
 'products', COALESCE(summary->'products', '[]'::jsonb),
 'selectedProducts', COALESCE(summary->'selectedProducts', '[]'::jsonb)),
 header = jsonb_set(header, '{appearance}', p.store_card #> '{profile,appearance}'),
 card = jsonb_set(card, '{appearance}', p.store_card #> '{profile,appearance}')
 || jsonb_build_object('products', COALESCE(card->'products', '[]'::jsonb)),
 profile_version = GREATEST(s.profile_version, p.version)
 FROM participants p
 WHERE p.tenant_id = s.tenant_id;

INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
 SELECT 'participant.settings.sync', tenant_id, 'participant.settings.sync:' || tenant_id,
 jsonb_build_object('tenantId', tenant_id), now(), now() FROM participants
 ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO NOTHING;

INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
 SELECT 'participant.store-card.refresh', tenant_id, 'participant.store-card.refresh:' || tenant_id,
 '{}'::jsonb, now(), now() FROM participants
 ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO NOTHING;

-- Поколение меняется транзакционно с готовой карточкой; старый Redis-ключ после этого не читается.
ALTER TABLE store_cards ADD COLUMN cache_signature text NOT NULL DEFAULT '';
CREATE TABLE store_card_cache_generation (
 id integer PRIMARY KEY CHECK (id = 1),
 version bigint NOT NULL DEFAULT 1
);
INSERT INTO store_card_cache_generation (id) VALUES (1);

-- +goose StatementBegin
CREATE FUNCTION invalidate_store_card_cache() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER store_card_cache_changed AFTER INSERT OR UPDATE OR DELETE ON store_cards
 FOR EACH ROW EXECUTE FUNCTION invalidate_store_card_cache();

-- +goose Down
-- Не возвращаем удалённые настройки двух зон и не меняем выбранный магазином цвет.
DROP TRIGGER store_card_cache_changed ON store_cards;
DROP FUNCTION invalidate_store_card_cache();
DROP TABLE store_card_cache_generation;
ALTER TABLE store_cards DROP COLUMN cache_signature;
