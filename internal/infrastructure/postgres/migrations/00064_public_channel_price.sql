-- +goose Up
ALTER TABLE catalog_products DROP CONSTRAINT catalog_products_card_unless_deleted;
ALTER TABLE catalog_products ADD CONSTRAINT catalog_products_card_unless_deleted
    CHECK (deleted OR card IS NOT NULL);
ALTER TABLE catalog_products ADD COLUMN base_price bigint CHECK (base_price >= 0);
UPDATE catalog_products SET
    price = (jsonb_path_query_first(price_profiles, '$[*] ? (@.key == "marketplace:default")')->>'effectivePrice')::bigint,
    base_price = (jsonb_path_query_first(price_profiles, '$[*] ? (@.key == "marketplace:default")')->>'price')::bigint;
ALTER TABLE catalog_products
    DROP COLUMN default_price,
    DROP COLUMN price_profiles,
    DROP COLUMN promo_price,
    ADD CONSTRAINT catalog_public_price_pair CHECK ((price IS NULL) = (base_price IS NULL));
CREATE INDEX catalog_recommendations_category ON catalog_products
    (category_id, sales_count DESC, tenant_id, product_id) WHERE on_sale AND price IS NOT NULL;
CREATE INDEX catalog_recommendations_popular ON catalog_products
    (sales_count DESC, tenant_id, product_id) WHERE on_sale AND price IS NOT NULL;
CREATE INDEX catalog_recommendations_tenant_rank ON catalog_products (
    tenant_id, sales_count DESC, ratings_count DESC, average_rating DESC,
    updated_at DESC, (md5(tenant_id || '~' || product_id)), product_id
) WHERE on_sale AND price IS NOT NULL;

-- +goose Down
ALTER TABLE catalog_products
    DROP CONSTRAINT catalog_public_price_pair,
    ADD COLUMN price_profiles jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN promo_price bigint CHECK (promo_price >= 0);
UPDATE catalog_products SET
    price_profiles = CASE WHEN price IS NULL THEN '[]'::jsonb ELSE
        jsonb_build_array(jsonb_build_object('key', 'marketplace:default',
            'price', base_price, 'effectivePrice', price)) END,
    promo_price = price,
    price = base_price;
DROP INDEX catalog_recommendations_category;
DROP INDEX catalog_recommendations_popular;
DROP INDEX catalog_recommendations_tenant_rank;
ALTER TABLE catalog_products DROP COLUMN base_price,
    ADD COLUMN default_price bigint GENERATED ALWAYS AS (
        (jsonb_path_query_first(price_profiles, '$[*] ? (@.key == "marketplace:default")')->>'effectivePrice')::bigint
    ) STORED;
CREATE INDEX catalog_recommendations_category ON catalog_products
    (category_id, sales_count DESC, tenant_id, product_id) WHERE on_sale AND default_price IS NOT NULL;
CREATE INDEX catalog_recommendations_popular ON catalog_products
    (sales_count DESC, tenant_id, product_id) WHERE on_sale AND default_price IS NOT NULL;
CREATE INDEX catalog_recommendations_tenant_rank ON catalog_products (
    tenant_id, sales_count DESC, ratings_count DESC, average_rating DESC,
    updated_at DESC, (md5(tenant_id || '~' || product_id)), product_id
) WHERE on_sale AND default_price IS NOT NULL;
