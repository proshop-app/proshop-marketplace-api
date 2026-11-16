-- +goose Up
ALTER TABLE checkout_groups ADD COLUMN next_work_at timestamptz;
UPDATE checkout_groups
SET next_work_at = LEAST(
 CASE WHEN order_progress->>'OrderID' <> ''
  AND ((order_progress->>'Abandoned' = 'true' AND order_progress->>'ReconciliationRequired' = 'true')
   OR (order_progress->>'Abandoned' IS DISTINCT FROM 'true'
    AND COALESCE(order_progress->>'Status', '') NOT IN ('cancelled', 'returned')))
  THEN COALESCE((order_progress->>'NextPollAt')::timestamptz, '-infinity') END,
 CASE WHEN (CASE
  WHEN order_progress->>'Abandoned' = 'true' THEN order_progress->>'Status'
  WHEN COALESCE(order_progress->>'OrderID', '') = ''
   AND order_progress->>'CreateExhausted' = 'true' THEN 'rejected'
  WHEN order_progress->>'Status' = 'seller_unreachable'
   AND order_progress->>'OrderID' <> '' AND order_progress->>'LastOrderStatus' <> ''
   THEN order_progress->>'LastOrderStatus'
  ELSE order_progress->>'Status' END) IN
  ('cancelled', 'cancellation_requested', 'rejected', 'price_changed', 'delivery_unavailable', 'unconfirmed')
  AND order_progress->>'CartReturned' IS DISTINCT FROM 'true'
  THEN COALESCE((order_progress->>'NextReturnAt')::timestamptz, '-infinity') END
);
CREATE INDEX checkout_groups_next_work ON checkout_groups (next_work_at, checkout_id)
 WHERE next_work_at IS NOT NULL;
CREATE INDEX outbox_kind_due ON outbox (kind, next_at, tenant_id, id) WHERE done_at IS NULL;

-- +goose Down
DROP INDEX outbox_kind_due;
DROP INDEX checkout_groups_next_work;
ALTER TABLE checkout_groups DROP COLUMN next_work_at;
