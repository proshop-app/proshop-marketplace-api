-- +goose Up
-- Убираем закрепления и устаревший пункт готовности из серверного снимка.
WITH cards AS (
 SELECT tenant_id,
  CASE WHEN jsonb_typeof(store_card #> '{card,missing}') = 'array' THEN
   jsonb_set(store_card #- '{profile,showcase}', '{card}',
    (store_card->'card') || jsonb_build_object('missing', (store_card #> '{card,missing}') - 'showcase') ||
    CASE WHEN (store_card #> '{card,missing}') ? 'showcase' THEN
     jsonb_build_object('ready', ((store_card #> '{card,missing}') - 'showcase') = '[]'::jsonb)
    ELSE '{}'::jsonb END)
  ELSE store_card #- '{profile,showcase}' END AS cleaned
 FROM participants
)
UPDATE participants p
 SET store_card = c.cleaned,
 card_ready = CASE WHEN (p.store_card #> '{card,missing}') ? 'showcase'
  THEN (c.cleaned #> '{card,ready}')::boolean ELSE p.card_ready END,
 version = p.version + 1,
 updated_at = now()
 FROM cards c
 WHERE p.tenant_id = c.tenant_id AND p.store_card IS DISTINCT FROM c.cleaned;

-- Очищаем готовые ответы сразу, не дожидаясь пересчёта публикации карточки.
UPDATE store_cards s
 SET header = header - 'showcase',
 card = card - 'showcase',
 summary = summary - 'showcase',
 profile_version = GREATEST(s.profile_version, p.version)
 FROM participants p
 WHERE p.tenant_id = s.tenant_id;

INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
 SELECT 'participant.store-card.refresh', tenant_id, 'participant.store-card.refresh:' || tenant_id,
 '{}'::jsonb, now(), now() FROM participants
 ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO NOTHING;

-- +goose Down
-- Удалённые закрепления невосстановимы; старое требование готовности не возвращаем.
