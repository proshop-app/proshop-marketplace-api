-- +goose Up
-- Оформление карточки больше не наследует фирменный цвет Студии.
WITH profiles AS (
 SELECT tenant_id,
  jsonb_set(store_card, '{profile}',
   (CASE WHEN jsonb_typeof(store_card->'profile') = 'object' THEN store_card->'profile' ELSE '{}'::jsonb END)
   - 'color' - 'initial' || jsonb_build_object('appearance',
    COALESCE(NULLIF(store_card #> '{profile,appearance}', 'null'::jsonb),
     '{"background":"#125F67","logoBackground":"#E9EEE7","textColor":"white"}'::jsonb))) AS cleaned
 FROM participants
), cards AS (
 SELECT tenant_id,
  CASE WHEN jsonb_typeof(cleaned #> '{card,missing}') = 'array' THEN
   jsonb_set(cleaned, '{card}',
    (cleaned->'card') || jsonb_build_object('missing', (cleaned #> '{card,missing}') - 'brandColor') ||
    CASE WHEN (cleaned #> '{card,missing}') ? 'brandColor' THEN
     jsonb_build_object('ready', ((cleaned #> '{card,missing}') - 'brandColor') = '[]'::jsonb)
    ELSE '{}'::jsonb END)
  ELSE cleaned END AS cleaned
 FROM profiles
)
UPDATE participants p
 SET store_card = c.cleaned,
 card_ready = CASE WHEN (p.store_card #> '{card,missing}') ? 'brandColor'
  THEN (c.cleaned #> '{card,ready}')::boolean ELSE p.card_ready END,
 version = p.version + 1,
 updated_at = now()
 FROM cards c
 WHERE p.tenant_id = c.tenant_id AND p.store_card IS DISTINCT FROM c.cleaned;

-- Удаляем старые поля сразу из готовых ответов, до фоновой сверки и пересчёта.
UPDATE store_cards s
 SET header = (header - 'color' - 'initial') || jsonb_build_object('appearance', p.store_card #> '{profile,appearance}'),
 card = (card - 'color' - 'initial') || jsonb_build_object('appearance', p.store_card #> '{profile,appearance}'),
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

-- +goose Down
-- Старую зависимость от цвета Студии и удалённое требование готовности не восстанавливаем.
