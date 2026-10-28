-- История готовых поставок и откатов хранится рядом с версионной головой таксономии.
-- +goose Up
CREATE TABLE taxonomy_releases (
    release bigint PRIMARY KEY CHECK (release > 0 AND release <= 9007199254740991),
    checksum text NOT NULL CHECK (checksum ~ '^[a-f0-9]{64}$'),
    published_version bigint NOT NULL REFERENCES taxonomy_versions(version),
    previous_definition jsonb NOT NULL CHECK (jsonb_typeof(previous_definition) = 'object'),
    applied_at timestamptz NOT NULL DEFAULT now(),
    rolled_back_version bigint REFERENCES taxonomy_versions(version),
    rolled_back_at timestamptz,
    CHECK (rolled_back_version IS NULL OR rolled_back_version > published_version),
    CHECK ((rolled_back_version IS NULL) = (rolled_back_at IS NULL))
);

-- +goose Down
DROP TABLE taxonomy_releases;
