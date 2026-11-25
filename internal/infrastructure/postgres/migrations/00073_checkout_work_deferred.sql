-- Отложенная работа покупки: проход восстановления не смог её разобрать и перенёс срок. Признак
-- виден оператору в фильтре «зависшие» и снимается первым удачным сохранением покупки. Частичный
-- индекс держит только отложенные группы.
-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE checkout_groups ADD COLUMN IF NOT EXISTS work_deferred_at timestamptz;
CREATE INDEX CONCURRENTLY IF NOT EXISTS checkout_groups_deferred_idx ON checkout_groups (checkout_id)
    WHERE work_deferred_at IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS checkout_groups_deferred_idx;
ALTER TABLE checkout_groups DROP COLUMN IF EXISTS work_deferred_at;
