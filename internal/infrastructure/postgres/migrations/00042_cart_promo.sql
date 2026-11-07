-- +goose Up
ALTER TABLE carts ADD COLUMN promos jsonb NOT NULL DEFAULT '{}';
ALTER TABLE cart_lines ADD COLUMN discounts jsonb NOT NULL DEFAULT '[]';

ALTER TABLE checkout_groups ADD COLUMN tracking jsonb;

-- +goose Down
ALTER TABLE checkout_groups DROP COLUMN tracking;
ALTER TABLE cart_lines DROP COLUMN discounts;
ALTER TABLE carts DROP COLUMN promos;
