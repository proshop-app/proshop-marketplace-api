-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE IF NOT EXISTS moderation_bulk_idempotency (
 key_hash text PRIMARY KEY,
 response jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL DEFAULT (now() + interval '30 days')
);
CREATE INDEX IF NOT EXISTS moderation_bulk_idempotency_expires ON moderation_bulk_idempotency (expires_at);
-- Набор ключей отделён от исторических текстов причин для индексируемого точного отбора.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION moderation_reason_keys(reasons jsonb) RETURNS text[]
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
 SELECT COALESCE(array_agg(DISTINCT item->>'key' ORDER BY item->>'key'), ARRAY[]::text[])
 FROM jsonb_array_elements(
  CASE WHEN jsonb_typeof(reasons) = 'array' THEN reasons ELSE '[]'::jsonb END
 ) item
$$;
-- +goose StatementEnd
CREATE INDEX CONCURRENTLY IF NOT EXISTS moderation_case_probation ON moderation_cases (tenant_id, id)
 WHERE status = 'open' AND moderation_reason_keys(reasons) = ARRAY['moderation.probation']::text[];
CREATE INDEX CONCURRENTLY IF NOT EXISTS moderation_case_tenant_open ON moderation_cases (tenant_id, id) WHERE status = 'open';

CREATE INDEX CONCURRENTLY IF NOT EXISTS moderation_case_open_id ON moderation_cases (id) WHERE status = 'open';
CREATE INDEX CONCURRENTLY IF NOT EXISTS moderation_case_source_open_id ON moderation_cases (source, id) WHERE status = 'open';

CREATE INDEX CONCURRENTLY IF NOT EXISTS moderation_case_open_reasons ON moderation_cases
 USING gin (moderation_reason_keys(reasons)) WHERE status = 'open';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS moderation_case_open_reasons;
DROP INDEX CONCURRENTLY IF EXISTS moderation_case_source_open_id;
DROP INDEX CONCURRENTLY IF EXISTS moderation_case_open_id;
DROP TABLE IF EXISTS moderation_bulk_idempotency;
DROP INDEX CONCURRENTLY IF EXISTS moderation_case_tenant_open;
DROP INDEX CONCURRENTLY IF EXISTS moderation_case_probation;
DROP FUNCTION IF EXISTS moderation_reason_keys(jsonb);
