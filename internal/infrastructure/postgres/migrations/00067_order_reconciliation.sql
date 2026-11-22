-- +goose Up
-- Статусы оплаченного заказа приходят событием магазина; опрос остаётся только неоплаченным.
-- reconcile_until — срок суточной сверки пачкой: исполняемый заказ сверяется до смены статуса,
-- полученный — ограниченный срок. Полученные до миграции не имеют времени получения и в сверку не входят.
ALTER TABLE checkout_groups ADD COLUMN reconcile_until timestamptz;

UPDATE checkout_groups
SET reconcile_until = '9999-12-31T00:00:00Z'
WHERE order_progress->>'Abandoned' IS DISTINCT FROM 'true'
  AND COALESCE(order_progress->>'OrderID', '') <> ''
  AND (CASE
    WHEN order_progress->>'Status' = 'seller_unreachable'
      AND COALESCE(order_progress->>'LastOrderStatus', '') <> ''
      THEN order_progress->>'LastOrderStatus'
    ELSE order_progress->>'Status' END) IN ('paid', 'shipped');

UPDATE checkout_groups
SET next_work_at = NULL
WHERE next_work_at IS NOT NULL
  AND order_progress->>'Abandoned' IS DISTINCT FROM 'true'
  AND (CASE
    WHEN order_progress->>'Status' = 'seller_unreachable'
      AND COALESCE(order_progress->>'LastOrderStatus', '') <> ''
      THEN order_progress->>'LastOrderStatus'
    ELSE order_progress->>'Status' END) IN ('paid', 'shipped', 'delivered', 'returned');

-- Срок в индексе: ночной обход и снятие истёкших не читают строки таблицы ради фильтра.
CREATE INDEX checkout_groups_reconcile ON checkout_groups (tenant_id, checkout_id) INCLUDE (reconcile_until)
  WHERE reconcile_until IS NOT NULL;

-- +goose Down
DROP INDEX checkout_groups_reconcile;
ALTER TABLE checkout_groups DROP COLUMN reconcile_until;
