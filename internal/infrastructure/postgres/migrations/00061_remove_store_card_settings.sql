-- +goose Up
-- Настройки удаляются из снимка участника; готовность больше не зависит от этих разделов.
WITH cards AS (
 SELECT tenant_id,
  CASE WHEN jsonb_typeof(store_card #> '{card,missing}') = 'array' THEN
   jsonb_set(store_card - 'products' #- '{profile,appearance}', '{card}',
    (store_card->'card') || jsonb_build_object(
     'missing', (store_card #> '{card,missing}')
      - ARRAY['appearance', 'products', 'cardAppearance', 'cardProducts', 'brandColor']) ||
    CASE WHEN (store_card #> '{card,missing}') ?|
     ARRAY['appearance', 'products', 'cardAppearance', 'cardProducts', 'brandColor'] THEN
     jsonb_build_object('ready', ((store_card #> '{card,missing}')
      - ARRAY['appearance', 'products', 'cardAppearance', 'cardProducts', 'brandColor']) = '[]'::jsonb)
    ELSE '{}'::jsonb END)
  ELSE store_card - 'products' #- '{profile,appearance}' END AS cleaned
 FROM participants
)
UPDATE participants p
 SET store_card = c.cleaned,
 card_ready = CASE WHEN (p.store_card #> '{card,missing}') ?|
  ARRAY['appearance', 'products', 'cardAppearance', 'cardProducts', 'brandColor']
  THEN (c.cleaned #>> '{card,ready}')::boolean ELSE p.card_ready END,
 version = p.version + 1,
 updated_at = now()
 FROM cards c
 WHERE p.tenant_id = c.tenant_id AND p.store_card IS DISTINCT FROM c.cleaned;

-- В карточку сразу попадает сохранённая автоматическая тройка, включая прежде выключенные карточки.
UPDATE store_cards s
 SET summary = s.summary - 'selectedProducts',
 header = s.header - 'appearance',
 card = (s.card - 'appearance') || jsonb_build_object('products',
  CASE WHEN jsonb_typeof(s.summary->'products') = 'array' THEN s.summary->'products' ELSE '[]'::jsonb END),
 public = p.card_ready AND s.page_public,
 profile_version = GREATEST(s.profile_version, p.version)
 FROM participants p WHERE p.tenant_id = s.tenant_id;

-- Старые сериализованные карточки в Redis недоступны даже до фонового пересчёта.
UPDATE store_card_cache_generation SET version = version + 1 WHERE id = 1;
UPDATE store_card_cache_versions SET version = version + 1;

INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
 SELECT 'participant.settings.sync', tenant_id, 'participant.settings.sync:' || tenant_id,
 jsonb_build_object('tenantId', tenant_id), now(), now() FROM participants
 ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO NOTHING;

INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
 SELECT 'participant.store-card.refresh', tenant_id, 'participant.store-card.refresh:' || tenant_id,
 '{}'::jsonb, now(), now() FROM participants
 ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO NOTHING;

-- +goose Down
-- JSONB-колонки не удалялись. Down восстанавливает пустую структуру без прежних данных.
UPDATE participants SET store_card = jsonb_set(store_card, '{profile}',
 (CASE WHEN jsonb_typeof(store_card->'profile') = 'object' THEN store_card->'profile' ELSE '{}'::jsonb END)
 || '{"appearance":{}}'::jsonb) || '{"products":{"enabled":true,"productIds":[]}}'::jsonb;
UPDATE store_cards SET summary = summary || '{"selectedProducts":[]}'::jsonb,
 header = header || '{"appearance":{}}'::jsonb,
 card = card || '{"appearance":{}}'::jsonb;
