-- +goose Up
ALTER TABLE taxonomy_revalidation
    ADD COLUMN available_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN lease bigint NOT NULL DEFAULT 0;
DROP INDEX taxonomy_revalidation_pending_idx;
CREATE INDEX taxonomy_revalidation_pending_idx
    ON taxonomy_revalidation (available_at, version, category) WHERE NOT done;
CREATE TABLE taxonomy_revalidation_failures (
    tenant_id text NOT NULL,
    product_id text NOT NULL,
    product_version bigint NOT NULL,
    source_revision bigint NOT NULL,
    taxonomy_version bigint NOT NULL,
    diagnostic text NOT NULL,
    failed_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, product_id)
);

-- +goose Down
DROP TABLE taxonomy_revalidation_failures;
DROP INDEX taxonomy_revalidation_pending_idx;
CREATE INDEX taxonomy_revalidation_pending_idx
    ON taxonomy_revalidation (version, category) WHERE NOT done;
ALTER TABLE taxonomy_revalidation
    DROP COLUMN available_at,
    DROP COLUMN lease;
