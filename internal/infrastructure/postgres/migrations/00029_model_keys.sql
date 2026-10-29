-- +goose Up
ALTER TABLE catalog_products
    ADD COLUMN model_key text GENERATED ALWAYS AS (NULLIF(card->>'modelKey', '')) STORED,
    ADD COLUMN model_key_source text GENERATED ALWAYS AS (NULLIF(card->>'modelKeySource', '')) STORED;
CREATE INDEX catalog_products_model_key ON catalog_products (model_key) WHERE model_key IS NOT NULL;

-- +goose Down
DROP INDEX catalog_products_model_key;
ALTER TABLE catalog_products DROP COLUMN model_key, DROP COLUMN model_key_source;
