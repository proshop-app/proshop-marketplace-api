-- +goose Up
CREATE TABLE moderation_idempotency (
 key_hash text PRIMARY KEY,
 response jsonb NOT NULL,
 expires_at timestamptz NOT NULL
);
CREATE INDEX moderation_idempotency_expires ON moderation_idempotency (expires_at);

-- +goose Down
DROP TABLE moderation_idempotency;
