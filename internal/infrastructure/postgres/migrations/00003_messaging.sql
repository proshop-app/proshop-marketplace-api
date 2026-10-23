-- Служебные таблицы сообщений: outbox задач, отметки обработанных событий
-- Kafka и отравленные сообщения.

-- +goose Up
CREATE TABLE outbox (
    id         bigserial PRIMARY KEY,
    kind       text        NOT NULL,
    tenant_id  text        NOT NULL,
    dedup_key  text,
    payload    jsonb       NOT NULL,
    attempts   integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error text,
    next_at    timestamptz NOT NULL,
    done_at    timestamptz,
    created_at timestamptz NOT NULL
);

CREATE INDEX outbox_due_idx ON outbox (done_at, next_at);
CREATE INDEX outbox_tenant_due_idx ON outbox (tenant_id, next_at, id) WHERE done_at IS NULL;
-- ещё не взятая задача с тем же ключом не дублируется; взятая уже читает
-- состояние, поэтому новое изменение после взятия ставит новую задачу
CREATE UNIQUE INDEX outbox_pending_dedup_idx ON outbox (dedup_key) WHERE done_at IS NULL AND attempts = 0;

CREATE TABLE processed_events (
    id           text PRIMARY KEY,
    processed_at timestamptz NOT NULL
);

CREATE INDEX processed_events_processed_idx ON processed_events (processed_at);

CREATE TABLE parked_events (
    id              bigserial PRIMARY KEY,
    topic           text        NOT NULL,
    kafka_partition integer     NOT NULL,
    kafka_offset    bigint      NOT NULL,
    message_key     bytea,
    headers         jsonb       NOT NULL DEFAULT '{}',
    value           bytea,
    tenant_id       text,
    reason          text        NOT NULL,
    error           text        NOT NULL DEFAULT '',
    attempts        integer     NOT NULL DEFAULT 0,
    parked_at       timestamptz NOT NULL,
    CONSTRAINT parked_events_position_key UNIQUE (topic, kafka_partition, kafka_offset)
);

CREATE INDEX parked_events_parked_idx ON parked_events (parked_at);
CREATE INDEX parked_events_tenant_idx ON parked_events (tenant_id, parked_at);

-- +goose Down
DROP TABLE parked_events;
DROP TABLE processed_events;
DROP TABLE outbox;
