-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE IF NOT EXISTS moderation_bulk_idempotency (
 key_hash text PRIMARY KEY,
 response jsonb NOT NULL
);
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

-- +goose Down
DROP TABLE IF EXISTS moderation_bulk_idempotency;
DROP INDEX CONCURRENTLY IF EXISTS moderation_case_tenant_open;
DROP INDEX CONCURRENTLY IF EXISTS moderation_case_probation;
DROP FUNCTION moderation_reason_keys(jsonb);
