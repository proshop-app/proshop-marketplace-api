-- +goose Up
ALTER TABLE outbox
    ADD COLUMN unknown_failures integer,
    ADD COLUMN failed_at timestamptz,
    ADD COLUMN failure_reason text,
    ADD CONSTRAINT outbox_failure_state CHECK ((failed_at IS NULL) = (failure_reason IS NULL)),
    ADD CONSTRAINT outbox_failure_not_done CHECK (failed_at IS NULL OR done_at IS NULL);
DROP INDEX outbox_due_idx;
DROP INDEX outbox_tenant_due_idx;
CREATE INDEX outbox_due_idx ON outbox (next_at, id) WHERE done_at IS NULL AND failed_at IS NULL;
CREATE INDEX outbox_tenant_due_idx ON outbox (tenant_id, next_at, id) WHERE done_at IS NULL AND failed_at IS NULL;
CREATE INDEX outbox_failed_id_idx ON outbox (id) WHERE failed_at IS NOT NULL;

-- +goose Down
DROP INDEX outbox_failed_id_idx;
DROP INDEX outbox_tenant_due_idx;
DROP INDEX outbox_due_idx;
CREATE INDEX outbox_due_idx ON outbox (done_at, next_at);
CREATE INDEX outbox_tenant_due_idx ON outbox (tenant_id, next_at, id) WHERE done_at IS NULL;
ALTER TABLE outbox
    DROP CONSTRAINT outbox_failure_not_done,
    DROP CONSTRAINT outbox_failure_state,
    DROP COLUMN failure_reason,
    DROP COLUMN failed_at,
    DROP COLUMN unknown_failures;
