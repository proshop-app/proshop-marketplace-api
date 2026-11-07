-- +goose Up
CREATE TABLE buyer_profiles (
 buyer_id uuid PRIMARY KEY REFERENCES buyers(id) ON DELETE CASCADE,
 body jsonb NOT NULL,
 version bigint NOT NULL CHECK (version > 0)
);
CREATE TABLE buyer_favorites (
 buyer_id uuid NOT NULL REFERENCES buyers(id) ON DELETE CASCADE,
 tenant_id text NOT NULL,
 product_id text NOT NULL,
 snapshot jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (buyer_id, tenant_id, product_id)
);
CREATE TABLE buyer_price_watches (
 buyer_id uuid NOT NULL REFERENCES buyers(id) ON DELETE CASCADE,
 tenant_id text NOT NULL,
 product_id text NOT NULL,
 snapshot jsonb NOT NULL,
 price_at_watch bigint NOT NULL CHECK (price_at_watch >= 0),
 target_price bigint CHECK (target_price >= 0),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (buyer_id, tenant_id, product_id)
);
CREATE TABLE buyer_shop_subscriptions (
 buyer_id uuid NOT NULL REFERENCES buyers(id) ON DELETE CASCADE,
 tenant_id text NOT NULL REFERENCES participants(tenant_id) ON DELETE CASCADE,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (buyer_id, tenant_id)
);
CREATE INDEX buyer_shop_subscriptions_tenant_idx ON buyer_shop_subscriptions(tenant_id);
-- +goose Down
DROP TABLE buyer_shop_subscriptions;
DROP TABLE buyer_price_watches;
DROP TABLE buyer_favorites;
DROP TABLE buyer_profiles;
