-- +goose NO TRANSACTION
-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS catalog_recommendations_category ON catalog_products
    (category_id, sales_count DESC, tenant_id, product_id) WHERE on_sale AND price IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS catalog_recommendations_popular ON catalog_products
    (sales_count DESC, tenant_id, product_id) WHERE on_sale AND price IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS catalog_recommendations_tenant_rank ON catalog_products (
    tenant_id, sales_count DESC, ratings_count DESC, average_rating DESC,
    updated_at DESC, (md5(tenant_id || '~' || product_id)), product_id
) WHERE on_sale AND price IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS catalog_recommendations_category;
DROP INDEX CONCURRENTLY IF EXISTS catalog_recommendations_popular;
DROP INDEX CONCURRENTLY IF EXISTS catalog_recommendations_tenant_rank;
