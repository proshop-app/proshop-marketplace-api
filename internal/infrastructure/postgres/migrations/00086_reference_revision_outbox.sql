-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION reference.enqueue_dictionary_revision() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
DECLARE
    previous_revision bigint := -1;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        previous_revision := OLD.revision;
    ELSIF NEW.revision = 0 AND NOT EXISTS (
        SELECT 1 FROM reference.dictionaries WHERE code = NEW.code AND revision = 0
    ) THEN
        -- Импорт с содержимым уже объявит положительную ревизию.
        RETURN NULL;
    END IF;
    IF NEW.revision > previous_revision THEN
        INSERT INTO public.outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
        VALUES (
            'reference.changed', '', 'reference.changed:' || NEW.code || ':' || NEW.revision,
            jsonb_build_object(
                'schemaVersion', 1, 'dictionary', NEW.code,
                'revisionFrom', greatest(previous_revision, 0), 'revisionTo', NEW.revision,
                'entryIds', '[]'::jsonb, 'kinds', '[]'::jsonb, 'affectedKeys', '[]'::jsonb,
                'truncated', true
            ), now(), now()
        )
        -- В штатном сценарии приложение уже поставило событие со списком изменений.
        ON CONFLICT (dedup_key) WHERE attempts = 0 DO NOTHING;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER reference_revision_outbox
AFTER INSERT OR UPDATE OF revision ON reference.dictionaries
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION reference.enqueue_dictionary_revision();

-- 00085 и прежние записи уже зафиксированы. Однократно объявляем текущие головы,
-- чтобы получить и те изменения, для которых раньше не была поставлена задача.
INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
SELECT 'reference.changed', '', 'reference.changed:' || code || ':' || revision,
    jsonb_build_object(
        'schemaVersion', 1, 'dictionary', code,
        'revisionFrom', 0, 'revisionTo', revision,
        'entryIds', '[]'::jsonb, 'kinds', '[]'::jsonb, 'affectedKeys', '[]'::jsonb,
        'truncated', true
    ), now(), now()
FROM reference.dictionaries
WHERE revision > 0
ON CONFLICT (dedup_key) WHERE attempts = 0 DO NOTHING;

-- +goose Down
DROP TRIGGER reference_revision_outbox ON reference.dictionaries;
DROP FUNCTION reference.enqueue_dictionary_revision();
