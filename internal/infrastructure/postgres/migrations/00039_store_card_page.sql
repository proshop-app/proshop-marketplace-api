-- +goose Up
ALTER TABLE store_cards ADD COLUMN page_public boolean NOT NULL DEFAULT false;
INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
 SELECT 'participant.store-card.refresh', tenant_id, 'participant.store-card.refresh:' || tenant_id,
 '{}'::jsonb, now(), now() FROM participants
 ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO NOTHING;

-- +goose Down
ALTER TABLE store_cards DROP COLUMN page_public;
