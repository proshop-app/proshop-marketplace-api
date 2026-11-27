-- +goose Up
CREATE TABLE offer_index_rebuild (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    generation text NOT NULL,
    started_at timestamptz NOT NULL,
    tenant_id text NOT NULL DEFAULT '',
    product_id text NOT NULL DEFAULT '',
    indexed bigint NOT NULL DEFAULT 0,
    skipped bigint NOT NULL DEFAULT 0,
    complete boolean NOT NULL DEFAULT false
);

CREATE TABLE offer_index_tenant (
    tenant_id text PRIMARY KEY,
    revision bigint NOT NULL,
    target bigint NOT NULL,
    product_id text NOT NULL,
    complete boolean NOT NULL
);

DROP INDEX outbox_ordered_head_idx;
CREATE INDEX outbox_ordered_head_idx ON outbox (kind, tenant_id, id)
    WHERE failed_at IS NULL AND kind IN ('catalog.tenant.sync', 'participant.settings.sync',
        'moderation.participant.status', 'reference.changed', 'search-synonyms.changed');

-- +goose Down
DROP INDEX outbox_ordered_head_idx;
CREATE INDEX outbox_ordered_head_idx ON outbox (kind, tenant_id, id)
    WHERE failed_at IS NULL AND kind IN ('participant.settings.sync', 'moderation.participant.status',
        'reference.changed', 'search-synonyms.changed');
DROP TABLE offer_index_tenant;
DROP TABLE offer_index_rebuild;
