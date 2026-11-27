-- +goose Up
-- Позиции обхода продавцов относятся к единице хранения нового поколения.
DELETE FROM offer_index_tenant;

-- При переносе плитка сохраняет вклад ушедшего варианта и получает ещё одну ревизию.
CREATE TABLE catalog_tile_departures (
    tenant_id text NOT NULL,
    tile_key text NOT NULL,
    version bigint NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, tile_key)
);
CREATE INDEX catalog_tile_departures_updated ON catalog_tile_departures (updated_at);

-- +goose StatementBegin
CREATE FUNCTION catalog_tile_departure() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    signals_version bigint;
    verdict_version bigint;
BEGIN
    SELECT version INTO signals_version FROM offers WHERE tenant_id = OLD.tenant_id AND product_id = OLD.product_id;
    SELECT version INTO verdict_version FROM moderation_verdicts
    WHERE tenant_id = OLD.tenant_id AND product_id = OLD.product_id;
    INSERT INTO catalog_tile_departures (tenant_id, tile_key, version, updated_at)
    VALUES (OLD.tenant_id, OLD.tile_key,
        OLD.version + COALESCE(signals_version, 0) + COALESCE(verdict_version, 0) + 1,
        clock_timestamp())
    ON CONFLICT (tenant_id, tile_key) DO UPDATE
    SET version = catalog_tile_departures.version + EXCLUDED.version,
        updated_at = GREATEST(catalog_tile_departures.updated_at, EXCLUDED.updated_at);
    RETURN NULL;
END;
$$;

CREATE FUNCTION catalog_tile_lock_member() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    -- Изменение источника и перенос варианта сериализуются на строке товара.
    PERFORM 1 FROM catalog_products
    WHERE tenant_id = NEW.tenant_id AND product_id = NEW.product_id FOR NO KEY UPDATE;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER catalog_tile_departure AFTER UPDATE ON catalog_products
    FOR EACH ROW WHEN (OLD.tile_key IS DISTINCT FROM NEW.tile_key)
    EXECUTE FUNCTION catalog_tile_departure();

CREATE TRIGGER catalog_tile_offer_revision AFTER INSERT OR UPDATE OF version ON offers
    FOR EACH ROW EXECUTE FUNCTION catalog_tile_lock_member();

CREATE TRIGGER catalog_tile_verdict_revision AFTER INSERT OR UPDATE OF version ON moderation_verdicts
    FOR EACH ROW EXECUTE FUNCTION catalog_tile_lock_member();

-- +goose Down
DROP TRIGGER catalog_tile_verdict_revision ON moderation_verdicts;
DROP TRIGGER catalog_tile_offer_revision ON offers;
DROP TRIGGER catalog_tile_departure ON catalog_products;
DROP FUNCTION catalog_tile_lock_member();
DROP FUNCTION catalog_tile_departure();
DROP TABLE catalog_tile_departures;
