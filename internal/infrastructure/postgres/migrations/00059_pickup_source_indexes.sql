-- +goose NO TRANSACTION

-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS participants_pickup_cdek
    ON participants (tenant_id)
    WHERE stage = 'connected' AND enabled AND delivery_valid AND (delivery_settings->'carriers') ? 'cdek';
CREATE INDEX CONCURRENTLY IF NOT EXISTS participants_pickup_yandex
    ON participants (tenant_id)
    WHERE stage = 'connected' AND enabled AND delivery_valid AND (delivery_settings->'carriers') ? 'yandex';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS participants_pickup_cdek;
DROP INDEX CONCURRENTLY IF EXISTS participants_pickup_yandex;
