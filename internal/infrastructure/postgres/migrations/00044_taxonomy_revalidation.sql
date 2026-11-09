-- +goose Up
-- Старые установки уже проверены синхронно: незавершённого прохода нет (version=0, done=true).
-- Сводки соответствуют существующей голове; на пустой установке это версия 0.
CREATE TABLE taxonomy_revalidation (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 cards_version bigint NOT NULL DEFAULT 0,
 cards_dirty boolean NOT NULL DEFAULT false,
 version bigint NOT NULL DEFAULT 0,
 tenant_id text NOT NULL DEFAULT '',
 product_id text NOT NULL DEFAULT '',
 done boolean NOT NULL DEFAULT true
);
INSERT INTO taxonomy_revalidation (singleton, cards_version)
 SELECT true, version FROM taxonomy_head;

-- +goose Down
DROP TABLE taxonomy_revalidation;
