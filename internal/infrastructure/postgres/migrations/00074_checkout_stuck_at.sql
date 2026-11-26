-- +goose Up
SET LOCAL lock_timeout = '10s';

ALTER TABLE checkout_groups
    ADD COLUMN stuck_at timestamptz;

-- Ключ видимой покупки хранится в её первой группе, чтобы отсрочка не зависела от записи заголовка.
WITH waiting AS (
    SELECT checkout_id, min(CASE
        WHEN work_deferred_at IS NOT NULL OR order_progress->>'ReconciliationRequired' = 'true'
            THEN '0001-01-01 00:00:00+00'::timestamptz
        WHEN order_progress->>'AwaitingSince' ~
            '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}([.][0-9]{1,9})?(Z|[+-][0-9]{2}:[0-9]{2})$'
            AND pg_input_is_valid(order_progress->>'AwaitingSince', 'timestamptz')
            THEN (order_progress->>'AwaitingSince')::timestamptz
        ELSE '0001-01-01 00:00:00+00'::timestamptz
    END) AS stuck_at
    FROM checkout_groups
    WHERE work_deferred_at IS NOT NULL
        OR order_progress->>'ReconciliationRequired' = 'true'
        OR order_progress->>'Status' = 'awaiting_payment'
    GROUP BY checkout_id
)
UPDATE checkout_groups g SET stuck_at = w.stuck_at
FROM waiting w JOIN checkouts c ON c.id = w.checkout_id
WHERE (c.status NOT IN ('draft', 'confirmed') OR c.order_context->>'CartConsumed' = 'true')
    AND g.checkout_id = w.checkout_id
    AND g.tenant_id = (
        SELECT a.tenant_id FROM checkout_groups a WHERE a.checkout_id = w.checkout_id
        ORDER BY a.tenant_id COLLATE "C" LIMIT 1
    );

CREATE UNIQUE INDEX checkout_groups_one_stuck_idx ON checkout_groups (checkout_id) WHERE stuck_at IS NOT NULL;

-- Некорректный срок попадает в начало обхода с конечным ключом, а строка помечается ошибкой.
CREATE INDEX checkout_groups_stuck_cursor_idx ON checkout_groups ((
    CASE WHEN stuck_at >= '0001-01-01 00:00:00+00'::timestamptz AND stuck_at < '10000-01-01 00:00:00+00'::timestamptz
        THEN stuck_at ELSE '0001-01-01 00:00:00+00'::timestamptz END
), checkout_id) WHERE stuck_at IS NOT NULL;

DROP INDEX checkout_groups_stuck_idx;
DROP INDEX checkout_groups_deferred_idx;

-- +goose Down
SET LOCAL lock_timeout = '10s';

CREATE INDEX checkout_groups_stuck_idx ON checkout_groups (checkout_id)
    WHERE order_progress->>'ReconciliationRequired' = 'true' OR order_progress->>'Status' = 'awaiting_payment';
CREATE INDEX checkout_groups_deferred_idx ON checkout_groups (checkout_id)
    WHERE work_deferred_at IS NOT NULL;
ALTER TABLE checkout_groups DROP COLUMN stuck_at;
