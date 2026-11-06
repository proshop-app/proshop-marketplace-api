-- +goose Up
ALTER TABLE catalog_products
 ADD COLUMN average_rating numeric(2,1) NOT NULL DEFAULT 0 CHECK (average_rating BETWEEN 0 AND 5),
 ADD COLUMN ratings_count bigint NOT NULL DEFAULT 0 CHECK (ratings_count >= 0),
 ADD COLUMN category_id text GENERATED ALWAYS AS (card->'publication'->>'categoryId') STORED,
 ADD COLUMN on_sale boolean GENERATED ALWAYS AS (
  NOT deleted AND COALESCE((card->>'visible')::boolean, false)
  AND COALESCE((card->'publication'->>'published')::boolean, false)
 ) STORED,
 -- Ключ синхронизирован с catalog.DefaultPriceProfile.
 ADD COLUMN default_price bigint GENERATED ALWAYS AS (
  (jsonb_path_query_first(price_profiles, '$[*] ? (@.key == "marketplace:default")')->>'effectivePrice')::bigint
 ) STORED,
 ADD COLUMN variant_group text GENERATED ALWAYS AS (
  CASE WHEN COALESCE(card->>'variantsId', '') = '' THEN 'product:' || product_id
  ELSE 'variants:' || (card->>'variantsId') END
 ) STORED;

CREATE INDEX catalog_recommendations_category ON catalog_products
 (category_id, sales_count DESC, tenant_id, product_id) WHERE on_sale AND default_price IS NOT NULL;
CREATE INDEX catalog_recommendations_popular ON catalog_products
 (sales_count DESC, tenant_id, product_id) WHERE on_sale AND default_price IS NOT NULL;
CREATE INDEX catalog_variant_group ON catalog_products (tenant_id, variant_group);

-- +goose Down
DROP INDEX catalog_recommendations_category;
DROP INDEX catalog_recommendations_popular;
DROP INDEX catalog_variant_group;
ALTER TABLE catalog_products
 DROP COLUMN average_rating,
 DROP COLUMN ratings_count,
 DROP COLUMN category_id,
 DROP COLUMN on_sale,
 DROP COLUMN default_price,
 DROP COLUMN variant_group;
