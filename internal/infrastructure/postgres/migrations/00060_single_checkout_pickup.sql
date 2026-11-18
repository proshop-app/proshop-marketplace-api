-- +goose Up
-- Один шаг с остановкой оформления: у новых покупок выбор хранится только в checkouts.
-- Незавершённый черновик без заказов выбирает единый пункт и подтверждает расчёты заново.
UPDATE checkouts c
SET status = 'draft',
    carrier = NULL,
    pickup_point_id = NULL,
    delivery = NULL,
    confirmed_at = NULL,
    version = version + 1
WHERE status IN ('draft', 'confirmed') AND NOT EXISTS (
    SELECT 1 FROM checkout_groups g WHERE g.checkout_id = c.id
    AND coalesce(g.order_progress->>'OrderID', '') <> ''
);
UPDATE checkout_groups g
SET quote_status = NULL,
    quote_reason = NULL,
    delivery_cost = NULL,
    min_days = NULL,
    max_days = NULL,
    quote_expires_at = NULL
FROM checkouts c
WHERE c.id = g.checkout_id AND c.status = 'draft' AND c.carrier IS NULL;
-- У исторического снимка с разными пунктами нет единственного достоверного адреса.
-- Не показываем последний выбранный пункт как адрес всех старых посылок; заказы магазинов неизменны.
UPDATE checkouts c
SET carrier = NULL,
    pickup_point_id = NULL,
    delivery = NULL,
    version = version + 1
WHERE c.id IN (
    SELECT g.checkout_id FROM checkout_groups g WHERE g.order_progress ? 'Delivery'
    GROUP BY g.checkout_id
    HAVING count(DISTINCT jsonb_build_array(
        g.order_progress->'Delivery'->>'Carrier',
        g.order_progress->'Delivery'->>'PickupPointCode',
        lower(trim(g.order_progress->'Delivery'->>'City'))
    )) > 1
);
UPDATE checkout_groups SET order_progress = order_progress - 'Delivery'
WHERE order_progress ? 'Delivery';
-- Неисполненные группы старых черновиков больше не содержат редактируемый выбор служб.
-- Реальное подключение каждой службы повторно проверяет магазин при расчёте.
UPDATE checkout_groups g
SET order_progress = jsonb_set(order_progress, '{Carriers}', '["cdek","yandex"]'::jsonb)
FROM checkouts c
WHERE c.id = g.checkout_id AND c.status = 'draft' AND c.carrier IS NULL;

-- Новый обязательный состав интеграций проверяется штатной сверкой до открытия трафика.
INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
 SELECT 'participant.settings.sync', tenant_id, 'participant.settings.sync:' || tenant_id,
 jsonb_build_object('tenantId', tenant_id), now(), now() FROM participants
 ON CONFLICT (dedup_key) WHERE done_at IS NULL AND attempts = 0 DO NOTHING;

-- Службы обязательны всем: источник зависит от видимости, а не сохранённого выбора службы.
DROP INDEX IF EXISTS participants_pickup_cdek;
DROP INDEX IF EXISTS participants_pickup_yandex;
CREATE INDEX participants_pickup_visible ON participants (tenant_id)
    WHERE stage = 'connected' AND enabled AND delivery_valid;

-- +goose Down
-- Миграция данных необратима по решению владельца: повторный выбор возможен на новом приложении.
SELECT 1;
