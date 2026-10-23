-- Справочники пунктов выдачи перевозчиков и аренда фоновых задач.

-- +goose Up
CREATE TABLE pickup_points (
    carrier      text             NOT NULL,
    code         text             NOT NULL,
    name         text             NOT NULL,
    address      text             NOT NULL,
    city         text             NOT NULL DEFAULT '',
    lat          double precision NOT NULL CHECK (lat BETWEEN -90 AND 90),
    lon          double precision NOT NULL CHECK (lon BETWEEN -180 AND 180),
    schedule     text             NOT NULL DEFAULT '',
    payload      jsonb,
    active       boolean          NOT NULL,
    refreshed_at timestamptz      NOT NULL,
    PRIMARY KEY (carrier, code)
);

CREATE INDEX pickup_points_stale_idx ON pickup_points (carrier, refreshed_at) WHERE active;

CREATE TABLE job_leases (
    name   text PRIMARY KEY,
    holder text        NOT NULL,
    until  timestamptz NOT NULL
);

-- +goose Down
DROP TABLE job_leases;
DROP TABLE pickup_points;
