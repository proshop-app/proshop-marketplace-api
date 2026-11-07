-- +goose Up
-- Снимок хранит готовность в card.missing. Исправляем её только там, где
-- единственным устаревшим требованием мог быть удалённый пункт.
WITH profiles AS (
 SELECT tenant_id,
  CASE WHEN jsonb_typeof(store_card #> '{card,missing}') = 'array' THEN
   jsonb_set(store_card #- '{profile,slogan}', '{card}',
    (store_card->'card') || jsonb_build_object('missing', (store_card #> '{card,missing}') - 'slogan') ||
    CASE WHEN (store_card #> '{card,missing}') ? 'slogan' THEN
     jsonb_build_object('ready', ((store_card #> '{card,missing}') - 'slogan') = '[]'::jsonb)
    ELSE '{}'::jsonb END)
  ELSE store_card #- '{profile,slogan}' END AS cleaned
 FROM participants
), cards AS (
 SELECT tenant_id,
  CASE WHEN jsonb_typeof(cleaned->'hidden') = 'array' THEN
   jsonb_set(cleaned, '{hidden}', (
    SELECT COALESCE(jsonb_agg(item ORDER BY position), '[]'::jsonb)
    FROM jsonb_array_elements(cleaned->'hidden') WITH ORDINALITY AS entries(item, position)
    WHERE item->>'key' IS DISTINCT FROM 'slogan'
   ))
  ELSE cleaned END AS cleaned
 FROM profiles
)
UPDATE participants p
 SET store_card = c.cleaned,
 card_ready = CASE WHEN (p.store_card #> '{card,missing}') ? 'slogan'
  THEN (c.cleaned #> '{card,ready}')::boolean ELSE p.card_ready END,
 version = p.version + 1,
 updated_at = now()
 FROM cards c
 WHERE p.tenant_id = c.tenant_id AND p.store_card IS DISTINCT FROM c.cleaned;

-- Публичные JSON и внутренняя сводка читаются до фоновой пересборки.
UPDATE store_cards s
 SET header = header - 'slogan',
 card = card - 'slogan',
 summary = CASE WHEN jsonb_typeof(summary->'hidden') = 'array' THEN
  jsonb_set(summary, '{hidden}', (
   SELECT COALESCE(jsonb_agg(item ORDER BY position), '[]'::jsonb)
   FROM jsonb_array_elements(summary->'hidden') WITH ORDINALITY AS entries(item, position)
   WHERE item->>'key' IS DISTINCT FROM 'slogan'
  ))
 ELSE summary END,
 -- Старый пересчёт не должен вернуть удалённые основания скрытия.
 profile_version = GREATEST(s.profile_version, p.version)
 FROM participants p
 WHERE p.tenant_id = s.tenant_id;

INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
 SELECT 'participant.store-card.refresh', tenant_id, 'participant.store-card.refresh:' || tenant_id,
 '{}'::jsonb, now(), now() FROM participants
 ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO NOTHING;

-- +goose Down
-- Удалённые значения и причины скрытия невосстановимы. Откат данных — no-op:
-- возвращать старое требование готовности без исходного снимка нельзя.
