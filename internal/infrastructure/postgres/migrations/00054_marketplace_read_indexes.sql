-- +goose NO TRANSACTION
-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS moderation_cases_offer_history
 ON moderation_cases (tenant_id, product_id, id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS moderation_decisions_case_history
 ON moderation_decisions (case_id, at DESC, id DESC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS promotion_decisions_campaign_history
 ON promotion_decisions (campaign_id, id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS promotion_campaigns_storefront
 ON promotion_campaigns (placement_code, category_id, date_to, date_from)
 WHERE status = 'active';
CREATE INDEX CONCURRENTLY IF NOT EXISTS promotion_campaigns_tick
 ON promotion_campaigns (status, id, date_to) WHERE status IN ('active', 'approved', 'review');
CREATE INDEX CONCURRENTLY IF NOT EXISTS promotion_campaigns_offer_read
 ON promotion_campaigns (tenant_id, product_id, placement_code, date_to, date_from) WHERE status = 'active';
-- Порядок совпадает с курсором потокового пересчёта: варианты не сортируются заново для каждой страницы.
CREATE INDEX CONCURRENTLY IF NOT EXISTS catalog_products_store_summary
 ON catalog_products (tenant_id, (CASE WHEN COALESCE(card->>'variantsId', '') = ''
 THEN 'product:' || product_id ELSE 'variants:' || (card->>'variantsId') END COLLATE "C"), product_id COLLATE "C")
 WHERE NOT deleted AND (COALESCE(card->'marketplace'->>'published', 'false')::boolean
 OR COALESCE(card->'publication'->>'published', 'false')::boolean);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS catalog_products_store_summary;
DROP INDEX CONCURRENTLY IF EXISTS promotion_campaigns_offer_read;
DROP INDEX CONCURRENTLY IF EXISTS promotion_campaigns_tick;
DROP INDEX CONCURRENTLY IF EXISTS promotion_campaigns_storefront;
DROP INDEX CONCURRENTLY IF EXISTS promotion_decisions_campaign_history;
DROP INDEX CONCURRENTLY IF EXISTS moderation_decisions_case_history;
DROP INDEX CONCURRENTLY IF EXISTS moderation_cases_offer_history;
