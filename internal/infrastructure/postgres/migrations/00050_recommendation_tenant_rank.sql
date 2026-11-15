-- +goose NO TRANSACTION
-- +goose Up
-- LATERAL читает верх рейтинга каждого магазина без сортировки всех его товаров.
CREATE INDEX CONCURRENTLY catalog_recommendations_tenant_rank ON catalog_products (
    tenant_id,
    sales_count DESC,
    ratings_count DESC,
    average_rating DESC,
    updated_at DESC,
    (md5(tenant_id || '~' || product_id)),
    product_id
) WHERE on_sale AND default_price IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY catalog_recommendations_tenant_rank;
