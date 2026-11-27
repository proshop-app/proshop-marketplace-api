-- +goose Up
ALTER TABLE cart_lines ALTER COLUMN position TYPE bigint;

CREATE TABLE cart_add_operations (
    buyer_id uuid NOT NULL REFERENCES buyers(id) ON DELETE CASCADE,
    key text NOT NULL,
    offer_id text NOT NULL,
    quantity integer NOT NULL,
    owner uuid NOT NULL,
    lease_until timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    completed boolean NOT NULL DEFAULT false,
    PRIMARY KEY (buyer_id, key)
);

CREATE INDEX cart_add_operations_expiry_idx ON cart_add_operations (expires_at);

-- +goose Down
DROP TABLE cart_add_operations;
ALTER TABLE cart_lines ALTER COLUMN position TYPE integer;
