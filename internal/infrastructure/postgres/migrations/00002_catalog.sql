-- Проекция каталогов магазинов: снимки товаров и тегов по ревизии источника,
-- сигналы остатка и цены по времени события. Суммы — копейки.

-- +goose Up
CREATE TABLE catalog_products (
    tenant_id    text        NOT NULL,
    product_id   text        NOT NULL,
    revision     bigint      NOT NULL CHECK (revision > 0),
    deleted      boolean     NOT NULL,
    card         jsonb,
    price        bigint CHECK (price >= 0),
    promo_price  bigint CHECK (promo_price >= 0),
    available    integer     NOT NULL DEFAULT 0 CHECK (available >= 0),
    projected_at timestamptz NOT NULL,
    version      bigint      NOT NULL CHECK (version > 0),
    updated_at   timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, product_id),
    CONSTRAINT catalog_products_card_unless_deleted
        CHECK (deleted OR (card IS NOT NULL AND price IS NOT NULL))
);

CREATE INDEX catalog_products_updated_idx ON catalog_products (updated_at);

CREATE TABLE catalog_tags (
    tenant_id  text        NOT NULL,
    tag_id     text        NOT NULL,
    revision   bigint      NOT NULL CHECK (revision > 0),
    deleted    boolean     NOT NULL,
    value      text        NOT NULL DEFAULT '',
    version    bigint      NOT NULL CHECK (version > 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, tag_id)
);

CREATE TABLE offers (
    tenant_id  text        NOT NULL,
    product_id text        NOT NULL,
    available  integer CHECK (available >= 0),
    stock_at   timestamptz,
    price      bigint CHECK (price >= 0),
    price_at   timestamptz,
    version    bigint      NOT NULL CHECK (version > 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, product_id),
    CONSTRAINT offers_stock_pair CHECK ((available IS NULL) = (stock_at IS NULL)),
    CONSTRAINT offers_price_pair CHECK ((price IS NULL) = (price_at IS NULL))
);

CREATE INDEX offers_updated_idx ON offers (updated_at);

-- +goose Down
DROP TABLE offers;
DROP TABLE catalog_tags;
DROP TABLE catalog_products;
