-- +goose Up
CREATE TABLE taxonomy_versions (
    version bigint PRIMARY KEY CHECK (version > 0),
    definition jsonb NOT NULL,
    loaded_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE taxonomy_head (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    version bigint NOT NULL DEFAULT 0,
    definition jsonb NOT NULL DEFAULT '{"version":0,"categories":[]}'
);
INSERT INTO taxonomy_head (singleton) VALUES (true);
-- +goose Down
DROP TABLE taxonomy_head;
DROP TABLE taxonomy_versions;
