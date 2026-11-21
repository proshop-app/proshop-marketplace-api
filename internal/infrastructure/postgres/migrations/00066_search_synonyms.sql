-- +goose Up
CREATE TABLE reference.search_synonym_head (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    revision bigint NOT NULL DEFAULT 0,
    last_published text NOT NULL DEFAULT '',
    budget_day text NOT NULL DEFAULT '',
    checked integer NOT NULL DEFAULT 0 CHECK (checked BETWEEN 0 AND 100),
    model_calls integer NOT NULL DEFAULT 0 CHECK (model_calls BETWEEN 0 AND 4),
    model_input text NOT NULL DEFAULT '',
    blocked_terms text[] NOT NULL DEFAULT '{}',
    snapshot text NOT NULL DEFAULT '{"schemaVersion":1,"revision":0,"rules":[],"blockedTerms":[]}',
    CHECK (octet_length(snapshot) <= 524288)
);

INSERT INTO reference.search_synonym_head DEFAULT VALUES;

-- Редакционная привязка записи реестра только к одному листу и ожидаемому имени.
CREATE TABLE reference.search_synonym_registry_scopes (
    entry_id uuid PRIMARY KEY REFERENCES reference.entries (id) ON DELETE CASCADE,
    canonical_key text NOT NULL,
    category_id text NOT NULL,
    category_name text NOT NULL
);

CREATE TABLE reference.search_synonym_taxonomy_names (
    category_id text NOT NULL,
    canonical_name text NOT NULL,
    equivalent_name text NOT NULL,
    PRIMARY KEY (category_id, equivalent_name)
);

INSERT INTO reference.search_synonym_taxonomy_names (category_id, canonical_name, equivalent_name) VALUES
    ('computers.routers', 'Маршрутизаторы', 'Роутеры'),
    ('computers.usb-hubs', 'USB-концентраторы', 'USB-хабы');

CREATE TABLE reference.search_synonym_candidates (
    id text PRIMARY KEY,
    phase text NOT NULL CHECK (phase IN ('candidate', 'quarantined', 'active', 'disabled')),
    version bigint NOT NULL CHECK (version > 0),
    expires_at timestamptz NOT NULL,
    next_check timestamptz NOT NULL,
    passed_at timestamptz,
    body jsonb NOT NULL
);

CREATE INDEX search_synonym_due ON reference.search_synonym_candidates (phase, next_check, id)
    WHERE phase IN ('quarantined', 'active');
CREATE INDEX search_synonym_live ON reference.search_synonym_candidates (expires_at)
    WHERE phase IN ('candidate', 'quarantined');
CREATE INDEX search_synonym_ready ON reference.search_synonym_candidates (passed_at, id)
    WHERE phase = 'quarantined' AND passed_at IS NOT NULL;

CREATE TABLE reference.search_synonym_rules (
    id text PRIMARY KEY REFERENCES reference.search_synonym_candidates (id),
    category_id text NOT NULL,
    from_term text NOT NULL,
    to_term text NOT NULL,
    UNIQUE (category_id, from_term)
);

CREATE TABLE reference.search_synonym_transitions (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    candidate_id text NOT NULL,
    from_phase text NOT NULL,
    to_phase text NOT NULL,
    at timestamptz NOT NULL,
    actor text NOT NULL,
    reason text NOT NULL
);

CREATE INDEX search_synonym_transitions_candidate ON reference.search_synonym_transitions (candidate_id, sequence);

CREATE TABLE reference.search_synonym_checks (
    batch_id text NOT NULL,
    candidate_id text NOT NULL,
    recorded_at timestamptz NOT NULL,
    body jsonb NOT NULL,
    PRIMARY KEY (candidate_id, batch_id)
);

CREATE INDEX search_synonym_brand_keys ON reference.entries (canonical_key) WHERE dictionary_code = 'brand';

-- +goose Down
DROP INDEX reference.search_synonym_brand_keys;
DROP TABLE reference.search_synonym_checks;
DROP TABLE reference.search_synonym_transitions;
DROP TABLE reference.search_synonym_rules;
DROP TABLE reference.search_synonym_candidates;
DROP TABLE reference.search_synonym_taxonomy_names;
DROP TABLE reference.search_synonym_registry_scopes;
DROP TABLE reference.search_synonym_head;
