-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE IF NOT EXISTS taxonomy_revalidation (
    version bigint NOT NULL REFERENCES taxonomy_versions(version),
    category text NOT NULL,
    tenant_cursor text NOT NULL DEFAULT '',
    product_cursor text NOT NULL DEFAULT '',
    processed bigint NOT NULL DEFAULT 0,
    done boolean NOT NULL DEFAULT false,
    available_at timestamptz NOT NULL DEFAULT now(),
    lease bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (version, category)
);
CREATE INDEX IF NOT EXISTS taxonomy_revalidation_pending_idx
    ON taxonomy_revalidation (available_at, version, category) WHERE NOT done;
CREATE TABLE IF NOT EXISTS taxonomy_revalidation_failures (
    tenant_id text NOT NULL,
    product_id text NOT NULL,
    product_version bigint NOT NULL,
    source_revision bigint NOT NULL,
    taxonomy_version bigint NOT NULL,
    diagnostic text NOT NULL,
    failed_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, product_id)
);

-- Исходная категория включает пока невалидные карточки и удалённые из таксономии категории.
-- CONCURRENTLY сохраняет приём событий при построении на существующем каталоге.
-- Повтор после прерванного CONCURRENTLY убирает возможный INVALID-индекс.
DROP INDEX CONCURRENTLY IF EXISTS catalog_products_taxonomy_category_idx;
CREATE INDEX CONCURRENTLY catalog_products_taxonomy_category_idx
    ON catalog_products ((card->'marketplace'->>'categoryId'), tenant_id, product_id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS catalog_products_taxonomy_category_idx;
DROP TABLE IF EXISTS taxonomy_revalidation_failures;
DROP TABLE IF EXISTS taxonomy_revalidation;
