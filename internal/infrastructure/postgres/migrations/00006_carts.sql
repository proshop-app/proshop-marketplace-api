-- Корзины покупателей: одна на покупателя, строки со снимком цены в копейках.

-- +goose Up
CREATE TABLE carts (
    id         uuid PRIMARY KEY,
    buyer_id   uuid        NOT NULL REFERENCES buyers (id),
    version    bigint      NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CONSTRAINT carts_buyer_key UNIQUE (buyer_id)
);

CREATE INDEX carts_expires_idx ON carts (expires_at);

CREATE TABLE cart_lines (
    cart_id    uuid        NOT NULL REFERENCES carts (id) ON DELETE CASCADE,
    tenant_id  text        NOT NULL,
    product_id text        NOT NULL,
    position   integer     NOT NULL,
    name       text        NOT NULL,
    quantity   integer     NOT NULL CHECK (quantity BETWEEN 1 AND 999),
    price      bigint      NOT NULL CHECK (price >= 0),
    issue      text        NOT NULL DEFAULT '' CHECK (issue IN ('', 'price_changed', 'not_enough_stock', 'unavailable')),
    added_at   timestamptz NOT NULL,
    checked_at timestamptz NOT NULL,
    PRIMARY KEY (cart_id, tenant_id, product_id)
);

-- +goose Down
DROP TABLE cart_lines;
DROP TABLE carts;
