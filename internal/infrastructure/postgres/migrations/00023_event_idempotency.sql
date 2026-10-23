-- +goose Up
CREATE TABLE operator_event_idempotency (
    key_hash text PRIMARY KEY,
    response jsonb NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX operator_event_idempotency_expiry_idx ON operator_event_idempotency (expires_at);
-- +goose Down
DROP TABLE operator_event_idempotency;
