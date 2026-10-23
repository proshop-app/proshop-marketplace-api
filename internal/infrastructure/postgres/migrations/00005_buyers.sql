-- Покупатели и проверки входа по звонку. Сессии живут в Redis.

-- +goose Up
CREATE TABLE buyers (
    id         uuid PRIMARY KEY,
    phone      text        NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT buyers_phone_key UNIQUE (phone)
);

CREATE TABLE phone_challenges (
    id                uuid PRIMARY KEY,
    phone             text        NOT NULL,
    provider_ref      text        NOT NULL,
    call_phone        text        NOT NULL,
    call_phone_pretty text        NOT NULL,
    status            text        NOT NULL CHECK (status IN ('pending', 'confirmed', 'expired', 'used')),
    checks            integer     NOT NULL DEFAULT 0 CHECK (checks >= 0),
    last_checked_at   timestamptz,
    expires_at        timestamptz NOT NULL,
    created_at        timestamptz NOT NULL,
    updated_at        timestamptz NOT NULL,
    version           bigint      NOT NULL CHECK (version > 0),
    CONSTRAINT phone_challenges_provider_ref_key UNIQUE (provider_ref)
);

CREATE INDEX phone_challenges_phone_idx ON phone_challenges (phone, created_at);
CREATE INDEX phone_challenges_expires_idx ON phone_challenges (expires_at);

-- +goose Down
DROP TABLE phone_challenges;
DROP TABLE buyers;
