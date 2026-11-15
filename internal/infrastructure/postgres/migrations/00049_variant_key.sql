-- +goose Up
ALTER TABLE catalog_products
    ADD COLUMN variant_key text GENERATED ALWAYS AS (NULLIF(card->>'variantKey', '')) STORED;

-- +goose Down
ALTER TABLE catalog_products DROP COLUMN variant_key;
