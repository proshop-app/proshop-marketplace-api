-- +goose Up
-- После очистки остаётся только идентификатор, чтобы запоздалые события не вернули магазин.
CREATE TABLE marketplace_tenant_deletions (
    tenant_id text PRIMARY KEY,
    step bigint NOT NULL DEFAULT 0,
    product_id text NOT NULL DEFAULT '',
    index_done boolean NOT NULL DEFAULT false,
    complete boolean NOT NULL DEFAULT false
);

ALTER TABLE moderation_idempotency ADD COLUMN tenant_id text
    GENERATED ALWAYS AS (response #>> '{Result,tenantId}') STORED;
CREATE INDEX moderation_idempotency_tenant_purge_idx ON moderation_idempotency (tenant_id, key_hash);
CREATE INDEX moderation_cases_tenant_purge_idx ON moderation_cases (tenant_id, id);
CREATE INDEX store_summary_changes_purge_idx ON store_summary_changes (tenant_id, id);
DROP INDEX outbox_ordered_head_idx;
CREATE INDEX outbox_ordered_head_idx ON outbox (kind, tenant_id, id)
    WHERE failed_at IS NULL AND kind IN ('catalog.tenant.sync', 'participant.tenant.purge', 'participant.settings.sync',
        'moderation.participant.status', 'reference.changed', 'search-synonyms.changed');

-- +goose StatementBegin
CREATE FUNCTION marketplace_open_tenants(shops text[], wait_for_delete boolean)
RETURNS SETOF text LANGUAGE plpgsql VOLATILE AS $$
DECLARE shop text;
BEGIN
    FOR shop IN SELECT DISTINCT unnest(shops) ORDER BY 1 LOOP
        IF shop = '' THEN
            RETURN NEXT shop;
            CONTINUE;
        END IF;

        IF wait_for_delete THEN
            PERFORM pg_advisory_xact_lock_shared(hashtextextended(shop, 83));
        ELSIF NOT pg_try_advisory_xact_lock_shared(hashtextextended(shop, 83)) THEN
            -- Постановщик outbox мог уже взять блокировку строки, которая нужна удалению.
            RAISE EXCEPTION 'идёт удаление магазина' USING ERRCODE = '40001';
        END IF;

        IF NOT EXISTS (SELECT 1 FROM marketplace_tenant_deletions WHERE tenant_id = shop) THEN
            RETURN NEXT shop;
        END IF;
    END LOOP;
END;
$$;

CREATE FUNCTION marketplace_store_task(task_kind text)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
    -- Задачи покупок продолжают обслуживать сохранённую историю покупателей.
    SELECT task_kind NOT IN ('participant.tenant.purge', 'search.purchase') AND task_kind NOT LIKE 'checkout.%'
$$;

CREATE OR REPLACE FUNCTION moderation_journal_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE shop text;
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('marketplace.purge_tenant', true) <> '' THEN
        IF TG_TABLE_NAME = 'moderation_decisions' THEN
            SELECT tenant_id INTO shop FROM moderation_cases WHERE id = OLD.case_id;
        ELSE
            shop := OLD.tenant_id;
        END IF;
        IF shop = current_setting('marketplace.purge_tenant', true)
            AND EXISTS (SELECT 1 FROM marketplace_tenant_deletions WHERE tenant_id = shop) THEN
            RETURN OLD;
        END IF;
    END IF;
    RAISE EXCEPTION 'журнал решений допускает только добавление';
END;
$$;

CREATE FUNCTION marketplace_guard_tenant_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM marketplace_open_tenants(ARRAY[NEW.tenant_id], false)) THEN
        RETURN NEW;
    END IF;
    -- В том числе отсекает результат фонового вычисления по уже удалённой проекции.
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE INDEX outbox_tenant_purge_idx ON outbox (tenant_id, next_at, id) WHERE marketplace_store_task(kind);

CREATE TRIGGER guard_tenant_case BEFORE INSERT ON moderation_cases
    FOR EACH ROW EXECUTE FUNCTION marketplace_guard_tenant_insert();
CREATE TRIGGER guard_tenant_campaign BEFORE INSERT ON promotion_campaigns
    FOR EACH ROW EXECUTE FUNCTION marketplace_guard_tenant_insert();
CREATE TRIGGER guard_tenant_revalidation BEFORE INSERT ON taxonomy_revalidation_failures
    FOR EACH ROW EXECUTE FUNCTION marketplace_guard_tenant_insert();

-- Удаление всего магазина снимает карточку сразу и удаляет очередь изменений самой очисткой.
DROP TRIGGER store_summary_product_changed ON catalog_products;
CREATE TRIGGER store_summary_product_changed BEFORE INSERT OR UPDATE ON catalog_products
    FOR EACH ROW EXECUTE FUNCTION record_store_summary_product();
CREATE TRIGGER store_summary_product_deleted BEFORE DELETE ON catalog_products FOR EACH ROW
    WHEN (OLD.tenant_id IS DISTINCT FROM current_setting('marketplace.purge_tenant', true))
    EXECUTE FUNCTION record_store_summary_product();
DROP TRIGGER store_summary_verdict_changed ON moderation_verdicts;
CREATE TRIGGER store_summary_verdict_changed AFTER INSERT OR UPDATE ON moderation_verdicts
    FOR EACH ROW EXECUTE FUNCTION record_store_summary_verdict();
CREATE TRIGGER store_summary_verdict_deleted AFTER DELETE ON moderation_verdicts FOR EACH ROW
    WHEN (OLD.tenant_id IS DISTINCT FROM current_setting('marketplace.purge_tenant', true))
    EXECUTE FUNCTION record_store_summary_verdict();

-- +goose Down
DROP TRIGGER store_summary_verdict_deleted ON moderation_verdicts;
DROP TRIGGER store_summary_verdict_changed ON moderation_verdicts;
CREATE TRIGGER store_summary_verdict_changed AFTER INSERT OR UPDATE OR DELETE ON moderation_verdicts
    FOR EACH ROW EXECUTE FUNCTION record_store_summary_verdict();
DROP TRIGGER store_summary_product_deleted ON catalog_products;
DROP TRIGGER store_summary_product_changed ON catalog_products;
CREATE TRIGGER store_summary_product_changed BEFORE INSERT OR UPDATE OR DELETE ON catalog_products
    FOR EACH ROW EXECUTE FUNCTION record_store_summary_product();

DROP TRIGGER guard_tenant_revalidation ON taxonomy_revalidation_failures;
DROP TRIGGER guard_tenant_campaign ON promotion_campaigns;
DROP TRIGGER guard_tenant_case ON moderation_cases;
DROP FUNCTION marketplace_guard_tenant_insert();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION moderation_journal_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'журнал решений допускает только добавление';
END;
$$;
-- +goose StatementEnd

DROP INDEX outbox_tenant_purge_idx;
DROP FUNCTION marketplace_store_task(text);
DROP FUNCTION marketplace_open_tenants(text[], boolean);
DROP INDEX outbox_ordered_head_idx;
CREATE INDEX outbox_ordered_head_idx ON outbox (kind, tenant_id, id)
    WHERE failed_at IS NULL AND kind IN ('catalog.tenant.sync', 'participant.settings.sync',
        'moderation.participant.status', 'reference.changed', 'search-synonyms.changed');
DROP INDEX store_summary_changes_purge_idx, moderation_cases_tenant_purge_idx;
ALTER TABLE moderation_idempotency DROP COLUMN tenant_id;
DROP TABLE marketplace_tenant_deletions;
