-- +goose Up
CREATE TABLE shop_directory_revision (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    revision bigint NOT NULL
);
INSERT INTO shop_directory_revision (revision) VALUES (1);

-- Последнее состояние на магазин, включая удаления.
CREATE TABLE shop_directory_changes (
    tenant_id text PRIMARY KEY,
    revision bigint NOT NULL,
    card jsonb
);
CREATE INDEX shop_directory_changes_revision ON shop_directory_changes (revision);

-- +goose StatementBegin
CREATE FUNCTION shop_directory_card(tenant text, slug text, name text, offers integer)
RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
    SELECT jsonb_build_object(
        'tenantId', tenant,
        'slug', slug,
        'name', name,
        'sizeTier', CASE
            WHEN offers >= 100 THEN 3
            WHEN offers >= 10 THEN 2
            WHEN offers >= 1 THEN 1
            ELSE 0
        END
    );
$$;

CREATE FUNCTION record_shop_directory_change(changed_tenant text, next_card jsonb)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    previous_card jsonb;
    next_revision bigint;
BEGIN
    -- Сохранение или удаление уже удерживает блокировку карточки этого магазина.
    SELECT card INTO previous_card FROM shop_directory_changes WHERE tenant_id = changed_tenant;
    IF previous_card IS NOT DISTINCT FROM next_card THEN
        RETURN;
    END IF;

    -- Блокировка до commit сохраняет порядок курсора при параллельных изменениях.
    UPDATE shop_directory_revision SET revision = revision + 1 RETURNING revision INTO next_revision;
    INSERT INTO shop_directory_changes (tenant_id, revision, card)
    VALUES (changed_tenant, next_revision, next_card)
    ON CONFLICT (tenant_id) DO UPDATE SET revision = EXCLUDED.revision, card = EXCLUDED.card;
END;
$$;

-- Сохранение публикует справочник в запросе репозитория, а каскад удаления участника проходит через этот триггер.
CREATE FUNCTION remove_shop_directory_card() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM record_shop_directory_change(OLD.tenant_id, NULL);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

INSERT INTO shop_directory_changes (tenant_id, revision, card)
    SELECT tenant_id, 1, shop_directory_card(tenant_id, slug, name, offers)
    FROM store_cards WHERE public;

CREATE TRIGGER shop_directory_deleted AFTER DELETE ON store_cards
    FOR EACH ROW WHEN (OLD.public) EXECUTE FUNCTION remove_shop_directory_card();

-- +goose Down
DROP TRIGGER shop_directory_deleted ON store_cards;
DROP FUNCTION remove_shop_directory_card();
DROP FUNCTION record_shop_directory_change(text, jsonb);
DROP FUNCTION shop_directory_card(text, text, text, integer);
DROP TABLE shop_directory_changes;
DROP TABLE shop_directory_revision;
