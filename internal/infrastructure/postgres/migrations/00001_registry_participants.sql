-- Зеркало реестра магазинов платформы и участники маркетплейса.

-- +goose Up
CREATE TABLE tenants (
    tenant_id  text PRIMARY KEY,
    slug       text        NOT NULL,
    status     text        NOT NULL CHECK (status IN ('creating', 'active', 'suspended', 'deleting', 'deleted')),
    cell       text        NOT NULL DEFAULT '',
    plan       jsonb,
    hosts      jsonb,
    event_at   timestamptz NOT NULL,
    version    bigint      NOT NULL CHECK (version > 0),
    updated_at timestamptz NOT NULL
);

CREATE INDEX tenants_updated_idx ON tenants (updated_at);

CREATE TABLE participants (
    tenant_id          text PRIMARY KEY REFERENCES tenants (tenant_id),
    slug               text        NOT NULL,
    name               text        NOT NULL,
    status             text        NOT NULL CHECK (status IN ('onboarding', 'connected')),
    enabled            boolean     NOT NULL DEFAULT false,
    payment_account_id text,
    version            bigint      NOT NULL CHECK (version > 0),
    created_at         timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL,
    CONSTRAINT participants_slug_key UNIQUE (slug),
    CONSTRAINT participants_payment_account_status
        CHECK ((status = 'connected') = (payment_account_id IS NOT NULL))
);

CREATE INDEX participants_created_idx ON participants (created_at, tenant_id);
CREATE INDEX participants_updated_idx ON participants (updated_at);

-- +goose Down
DROP TABLE participants;
DROP TABLE tenants;
