-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
DO $$
DECLARE
    code_to_repair text;
    last_id uuid;
    batch_ids uuid[];
    repair_revision bigint;
BEGIN
    FOR code_to_repair IN SELECT code FROM reference.dictionaries ORDER BY code LOOP
        last_id := NULL;
        LOOP
            -- Тот же замок, под которым приложение пишет реестр, журнал и версии.
            PERFORM 1 FROM reference.dictionaries WHERE code = code_to_repair FOR UPDATE;

            SELECT array_agg(id ORDER BY id) INTO batch_ids
            FROM (
                SELECT e.id
                FROM reference.entries e
                WHERE e.dictionary_code = code_to_repair AND (last_id IS NULL OR e.id > last_id)
                  AND NOT EXISTS (
                      SELECT 1 FROM reference.snapshot_versions v
                      WHERE v.dictionary_code = e.dictionary_code AND v.entry_id = e.id AND v.replaced_at IS NULL
                  )
                ORDER BY e.id
                LIMIT 500
            ) AS missing;

            IF batch_ids IS NOT NULL THEN
                -- Состояние реестра не меняется. Новая ревизия доставляет образы и через since,
                -- сохраняя прежние страницы на их закреплённой ревизии.
                INSERT INTO reference.changes (dictionary_code, entry_id, entry_ids, kind, actor, source, forward, inverse, at)
                VALUES (code_to_repair, batch_ids[1], batch_ids, 'snapshot.backfilled', 'migration', 'seed', '[]', '[]', now())
                RETURNING revision INTO repair_revision;

                INSERT INTO reference.snapshot_versions (dictionary_code, entry_id, revision, state)
                SELECT e.dictionary_code, e.id, repair_revision, jsonb_strip_nulls(jsonb_build_object(
                    'id', e.id,
                    'dictionary', e.dictionary_code,
                    'canonicalName', e.canonical_name,
                    'canonicalKey', e.canonical_key,
                    'status', e.status,
                    'mergedInto', e.merged_into,
                    'source', e.source,
                    'aliases', COALESCE(aliases.state, '[]'::jsonb),
                    'createdAt', e.created_at
                )) || jsonb_build_object('metadata', e.metadata)
                FROM reference.entries e
                LEFT JOIN LATERAL (
                    SELECT jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
                        'id', a.id,
                        'text', a.text,
                        'key', a.key,
                        'kind', a.kind,
                        'locale', a.locale,
                        'source', a.source,
                        'weight', a.weight
                    )) ORDER BY a.key) AS state
                    FROM reference.aliases a WHERE a.entry_id = e.id
                ) aliases ON true
                WHERE e.id = ANY(batch_ids);

                UPDATE reference.dictionaries SET revision = repair_revision, updated_at = now()
                WHERE code = code_to_repair;
            END IF;

            COMMIT;
            EXIT WHEN COALESCE(cardinality(batch_ids), 0) < 500;
            last_id := batch_ids[cardinality(batch_ids)];
        END LOOP;
    END LOOP;
END
$$;
-- +goose StatementEnd

-- Удалить также возможный невалидный индекс, оставшийся при прерывании CREATE INDEX.
DROP INDEX CONCURRENTLY IF EXISTS reference.reference_snapshot_versions_confirmed_idx;
CREATE INDEX CONCURRENTLY reference_snapshot_versions_confirmed_idx
    ON reference.snapshot_versions USING gist (dictionary_code, validity, position)
    WHERE state IS NOT NULL AND state->>'status' = 'confirmed';
-- Без статистики выражения планировщик недооценивает число подтверждённых записей
-- и вместо упорядоченного обхода GiST может прочитать и отсортировать весь словарь.
CREATE STATISTICS IF NOT EXISTS reference.reference_snapshot_versions_status_stats
    ON (state->>'status') FROM reference.snapshot_versions;
ANALYZE reference.snapshot_versions;
DROP INDEX CONCURRENTLY IF EXISTS reference.reference_snapshot_versions_full_idx;

-- +goose Down
-- Восстановленные образы и их ревизии уже могут быть прочитаны потребителями.
CREATE INDEX CONCURRENTLY IF NOT EXISTS reference_snapshot_versions_full_idx
    ON reference.snapshot_versions USING gist (dictionary_code, validity, position) WHERE state IS NOT NULL;
DROP INDEX CONCURRENTLY IF EXISTS reference.reference_snapshot_versions_confirmed_idx;
DROP STATISTICS IF EXISTS reference.reference_snapshot_versions_status_stats;
