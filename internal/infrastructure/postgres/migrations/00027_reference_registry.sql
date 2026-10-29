-- Реестр справочных сущностей: своя схема без join с таблицами площадки.
-- Ключ варианта уникален в словаре; журнал изменений только на добавление,
-- снимки и откат читают его образы. Частные записи магазинов и связи —
-- схемой на будущее этапа, сценариев для них ещё нет.
-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE SCHEMA reference;

CREATE TABLE reference.dictionaries (
    code        text PRIMARY KEY CHECK (code ~ '^[a-z][a-z0-9_]{0,31}$'),
    kind        text        NOT NULL CHECK (kind IN ('open', 'closed')),
    identity    boolean     NOT NULL DEFAULT false,
    alias_kinds text[]      NOT NULL,
    limits      jsonb       NOT NULL CHECK (jsonb_typeof(limits) = 'object'),
    -- голова журнала словаря и число записей меняются под блокировкой строки
    revision    bigint      NOT NULL DEFAULT 0 CHECK (revision >= 0),
    entries     bigint      NOT NULL DEFAULT 0 CHECK (entries >= 0),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE reference.entries (
    id              uuid PRIMARY KEY,
    dictionary_code text        NOT NULL REFERENCES reference.dictionaries (code),
    canonical_name  text        NOT NULL CHECK (canonical_name <> ''),
    canonical_key   text        NOT NULL CHECK (canonical_key <> ''),
    status          text        NOT NULL
        CHECK (status IN ('candidate', 'confirmed', 'merged', 'deprecated', 'rejected')),
    merged_into     uuid REFERENCES reference.entries (id),
    source          text        NOT NULL,
    metadata        jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(metadata) = 'object'),
    occurrences     bigint      NOT NULL DEFAULT 1 CHECK (occurrences >= 0),
    revision        bigint      NOT NULL CHECK (revision > 0),
    version         bigint      NOT NULL CHECK (version > 0),
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    CHECK ((status = 'merged') = (merged_into IS NOT NULL)),
    CHECK (merged_into IS DISTINCT FROM id)
);

-- очередь кандидатов и списки идут по частоте встреч
CREATE INDEX entries_queue_idx ON reference.entries (dictionary_code, status, occurrences DESC, canonical_key, id);
CREATE INDEX entries_merged_into_idx ON reference.entries (merged_into) WHERE merged_into IS NOT NULL;

CREATE TABLE reference.aliases (
    id              uuid PRIMARY KEY,
    entry_id        uuid     NOT NULL REFERENCES reference.entries (id) ON DELETE CASCADE,
    dictionary_code text     NOT NULL REFERENCES reference.dictionaries (code),
    text            text     NOT NULL CHECK (text <> ''),
    key             text     NOT NULL CHECK (key <> ''),
    kind            text     NOT NULL CHECK (kind IN ('spelling', 'translit', 'abbreviation', 'former', 'locale')),
    locale          text,
    source          text     NOT NULL,
    status          text     NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'proposed')),
    weight          smallint NOT NULL DEFAULT 0 CHECK (weight BETWEEN 0 AND 100),
    -- бренды всегда разные: одно написание принадлежит одной записи словаря
    CONSTRAINT aliases_dictionary_key_key UNIQUE (dictionary_code, key)
);

CREATE INDEX aliases_entry_idx ON reference.aliases (entry_id);
CREATE INDEX aliases_prefix_idx ON reference.aliases (dictionary_code, key text_pattern_ops);
CREATE INDEX aliases_trgm_idx ON reference.aliases USING gin (key gin_trgm_ops);

CREATE TABLE reference.relations (
    from_id    uuid        NOT NULL REFERENCES reference.entries (id) ON DELETE CASCADE,
    to_id      uuid        NOT NULL REFERENCES reference.entries (id) ON DELETE CASCADE,
    kind       text        NOT NULL CHECK (kind IN ('parent', 'sub_brand', 'successor')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (from_id, to_id, kind),
    CHECK (from_id <> to_id)
);

CREATE TABLE reference.changes (
    revision        bigserial PRIMARY KEY,
    dictionary_code text        NOT NULL REFERENCES reference.dictionaries (code),
    entry_id        uuid        NOT NULL,
    entry_ids       uuid[]      NOT NULL CHECK (cardinality(entry_ids) > 0),
    kind            text        NOT NULL,
    actor           text        NOT NULL,
    source          text        NOT NULL,
    reason          text        NOT NULL DEFAULT '',
    reverts         bigint REFERENCES reference.changes (revision),
    forward         jsonb       NOT NULL CHECK (jsonb_typeof(forward) = 'array'),
    inverse         jsonb       NOT NULL CHECK (jsonb_typeof(inverse) = 'array'),
    at              timestamptz NOT NULL
);

CREATE INDEX changes_dictionary_revision_idx ON reference.changes (dictionary_code, revision);
CREATE INDEX changes_dictionary_at_idx ON reference.changes (dictionary_code, at, revision);

-- +goose StatementBegin
CREATE FUNCTION reference.changes_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'журнал реестра только на добавление: откат — новая ревизия';
END
$$;
-- +goose StatementEnd

CREATE TRIGGER changes_append_only
    BEFORE UPDATE OR DELETE ON reference.changes
    FOR EACH ROW EXECUTE FUNCTION reference.changes_append_only();

CREATE TABLE reference.tenant_entries (
    id              uuid PRIMARY KEY,
    tenant_id       text        NOT NULL,
    dictionary_code text        NOT NULL REFERENCES reference.dictionaries (code),
    canonical_name  text        NOT NULL CHECK (canonical_name <> ''),
    canonical_key   text        NOT NULL CHECK (canonical_key <> ''),
    status          text        NOT NULL
        CHECK (status IN ('candidate', 'confirmed', 'merged', 'deprecated', 'rejected')),
    merged_into     uuid REFERENCES reference.tenant_entries (id),
    -- перевод частной записи в общую — явное действие оператора
    promoted_to     uuid REFERENCES reference.entries (id),
    source          text        NOT NULL,
    metadata        jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(metadata) = 'object'),
    revision        bigint      NOT NULL CHECK (revision > 0),
    version         bigint      NOT NULL CHECK (version > 0),
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    CHECK ((status = 'merged') = (merged_into IS NOT NULL))
);

CREATE INDEX tenant_entries_tenant_idx ON reference.tenant_entries (tenant_id, dictionary_code, status);

CREATE TABLE reference.tenant_aliases (
    id              uuid PRIMARY KEY,
    tenant_id       text     NOT NULL,
    dictionary_code text     NOT NULL REFERENCES reference.dictionaries (code),
    -- частный вариант магазина привязан к общей или к частной записи
    entry_id        uuid REFERENCES reference.entries (id) ON DELETE CASCADE,
    tenant_entry_id uuid REFERENCES reference.tenant_entries (id) ON DELETE CASCADE,
    text            text     NOT NULL CHECK (text <> ''),
    key             text     NOT NULL CHECK (key <> ''),
    kind            text     NOT NULL CHECK (kind IN ('spelling', 'translit', 'abbreviation', 'former', 'locale')),
    locale          text,
    source          text     NOT NULL,
    status          text     NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'proposed')),
    weight          smallint NOT NULL DEFAULT 0 CHECK (weight BETWEEN 0 AND 100),
    CHECK ((entry_id IS NULL) <> (tenant_entry_id IS NULL)),
    CONSTRAINT tenant_aliases_key_key UNIQUE (tenant_id, dictionary_code, key)
);

CREATE TABLE reference.deliveries (
    dictionary_code   text        NOT NULL REFERENCES reference.dictionaries (code),
    version           bigint      NOT NULL CHECK (version > 0 AND version <= 9007199254740991),
    checksum          text        NOT NULL CHECK (checksum ~ '^[a-f0-9]{64}$'),
    revision_from     bigint      NOT NULL CHECK (revision_from >= 0),
    revision_to       bigint      NOT NULL CHECK (revision_to >= revision_from),
    applied_at        timestamptz NOT NULL,
    rolled_back_at    timestamptz,
    rollback_revision bigint,
    PRIMARY KEY (dictionary_code, version),
    CHECK ((rolled_back_at IS NULL) = (rollback_revision IS NULL))
);

-- Присутствие записи в узлах дерева площадки по опубликованным предложениям.
-- Влияет только на порядок кандидатов и навигацию оператора, не на разрешение.
CREATE TABLE reference.entry_presence (
    entry_id    uuid        NOT NULL REFERENCES reference.entries (id) ON DELETE CASCADE,
    category_id text        NOT NULL CHECK (category_id <> ''),
    offers      integer     NOT NULL CHECK (offers >= 0),
    sellers     integer     NOT NULL CHECK (sellers >= 0),
    computed_at timestamptz NOT NULL,
    PRIMARY KEY (entry_id, category_id)
);

CREATE INDEX entry_presence_category_idx ON reference.entry_presence (category_id, offers DESC);

-- Первый открытый словарь плана реестра; код словаря ничего не значит для кода площадки.
INSERT INTO reference.dictionaries (code, kind, identity, alias_kinds, limits)
VALUES (
    'brand',
    'open',
    true,
    ARRAY['spelling', 'translit', 'abbreviation', 'former', 'locale'],
    '{"maxEntries": 1000000, "maxAliases": 50, "maxAliasLength": 120, "maxAliasWords": 8, "normalizer": "v1"}'
);

-- +goose Down
-- pg_trgm остаётся: расширение может понадобиться другим схемам базы.
DROP SCHEMA reference CASCADE;
