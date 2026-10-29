-- Ручные поисковые теги удалены из контракта товаров и поисковых запросов.
-- +goose Up
DROP TABLE catalog_tags;
UPDATE catalog_products SET card = card - 'tags' WHERE card ? 'tags';

-- +goose Down
-- Восстанавливает схему для прежнего кода; удалённые значения не восстанавливаются.
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
