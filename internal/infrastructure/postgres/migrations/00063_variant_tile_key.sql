-- +goose Up
DROP INDEX catalog_variant_group;
ALTER TABLE catalog_products DROP COLUMN variant_group;

ALTER TABLE catalog_products
    ADD COLUMN tile_key text GENERATED ALWAYS AS (
        COALESCE(NULLIF(card->>'tileKey', ''), product_id)
    ) STORED;

CREATE INDEX catalog_tile_key ON catalog_products (tenant_id, tile_key);

-- +goose Down
DROP INDEX catalog_tile_key;
ALTER TABLE catalog_products DROP COLUMN tile_key;
ALTER TABLE catalog_products
    ADD COLUMN variant_group text GENERATED ALWAYS AS (
        CASE WHEN COALESCE(card->>'variantsId', '') = '' THEN 'product:' || product_id
        ELSE 'variants:' || (card->>'variantsId') END
    ) STORED;
CREATE INDEX catalog_variant_group ON catalog_products (tenant_id, variant_group);
