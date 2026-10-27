-- +goose Up
ALTER TABLE catalog_products ADD COLUMN price_profiles jsonb NOT NULL DEFAULT '[]'::jsonb;

-- +goose Down
ALTER TABLE catalog_products DROP COLUMN price_profiles;
