-- Покупки до подтверждения: группы по продавцам с ключом заказа у магазина
-- и расчётом доставки. Суммы — копейки.

-- +goose Up
CREATE TABLE checkouts (
    id              uuid PRIMARY KEY,
    buyer_id        uuid        NOT NULL REFERENCES buyers (id),
    idempotency_key text        NOT NULL,
    status          text        NOT NULL CHECK (status IN ('draft', 'confirmed')),
    carrier         text,
    pickup_point_id text,
    delivery        jsonb,
    goods_total     bigint      NOT NULL CHECK (goods_total >= 0),
    delivery_total  bigint      NOT NULL CHECK (delivery_total >= 0),
    total           bigint      NOT NULL CHECK (total >= 0),
    version         bigint      NOT NULL CHECK (version > 0),
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    confirmed_at    timestamptz,
    CONSTRAINT checkouts_buyer_key_key UNIQUE (buyer_id, idempotency_key),
    CONSTRAINT checkouts_delivery_pair CHECK ((carrier IS NULL) = (pickup_point_id IS NULL))
);

CREATE INDEX checkouts_status_idx ON checkouts (status, updated_at);
CREATE INDEX checkouts_buyer_idx ON checkouts (buyer_id, created_at);

CREATE TABLE checkout_groups (
    checkout_id          uuid   NOT NULL REFERENCES checkouts (id) ON DELETE CASCADE,
    tenant_id            text   NOT NULL,
    external_checkout_id text   NOT NULL,
    lines                jsonb  NOT NULL,
    goods                bigint NOT NULL CHECK (goods >= 0),
    quote_status         text CHECK (quote_status IN ('available', 'unavailable')),
    quote_reason         text,
    delivery_cost        bigint CHECK (delivery_cost >= 0),
    subsidy              bigint CHECK (subsidy >= 0),
    buyer_delivery       bigint CHECK (buyer_delivery >= 0),
    min_days             integer,
    max_days             integer,
    quote_expires_at     timestamptz,
    PRIMARY KEY (checkout_id, tenant_id),
    CONSTRAINT checkout_groups_external_key UNIQUE (external_checkout_id)
);

-- +goose Down
DROP TABLE checkout_groups;
DROP TABLE checkouts;
