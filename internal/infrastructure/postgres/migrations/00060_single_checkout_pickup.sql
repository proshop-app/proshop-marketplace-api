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

-- +goose Down
-- Миграция данных необратима по решению владельца: повторный выбор возможен на новом приложении.
SELECT 1;
