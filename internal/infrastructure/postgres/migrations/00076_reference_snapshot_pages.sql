-- +goose Up
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE reference.snapshot_versions (
    dictionary_code text NOT NULL REFERENCES reference.dictionaries (code),
    entry_id uuid NOT NULL,
    position bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    revision bigint NOT NULL,
    replaced_at bigint,
    validity int8range GENERATED ALWAYS AS (int8range(revision, replaced_at)) STORED,
    state jsonb
);

CREATE UNIQUE INDEX reference_snapshot_versions_current_idx
    ON reference.snapshot_versions (dictionary_code, entry_id) WHERE replaced_at IS NULL;

-- Полный снимок пропускает удаления и версии вне закреплённой ревизии внутри индекса.
CREATE INDEX reference_snapshot_versions_full_idx
    ON reference.snapshot_versions USING gist (dictionary_code, validity, position) WHERE state IS NOT NULL;
CREATE INDEX reference_snapshot_versions_delta_idx
    ON reference.snapshot_versions USING gist (dictionary_code, validity, revision, position);

-- +goose Down
DROP TABLE reference.snapshot_versions;
