-- +goose Up
ALTER TABLE promotion_campaigns ADD COLUMN created_at timestamptz,
 ADD COLUMN updated_at timestamptz, ADD COLUMN product_id text;
UPDATE promotion_campaigns SET created_at = (state->>'CreatedAt')::timestamptz,
 updated_at = (state->>'UpdatedAt')::timestamptz,
 product_id = state->'Target'->'OfferID'->>'Product';
ALTER TABLE promotion_campaigns ALTER COLUMN created_at SET NOT NULL,
 ALTER COLUMN updated_at SET NOT NULL, ALTER COLUMN product_id SET NOT NULL;
CREATE INDEX promotion_created ON promotion_campaigns(created_at DESC, id DESC);
CREATE INDEX promotion_product ON promotion_campaigns(tenant_id, product_id);
ALTER TABLE promotion_views ADD COLUMN kind text NOT NULL DEFAULT 'view' CHECK (kind IN ('view','click'));
ALTER TABLE promotion_views DROP CONSTRAINT promotion_views_pkey;
ALTER TABLE promotion_views ADD PRIMARY KEY(campaign_id, day, visitor_hash, kind);
ALTER TABLE promotion_decisions DROP CONSTRAINT promotion_decisions_campaign_id_fkey;
ALTER TABLE promotion_decisions ADD FOREIGN KEY(campaign_id) REFERENCES promotion_campaigns(id) ON DELETE CASCADE;
-- +goose Down
ALTER TABLE promotion_decisions DROP CONSTRAINT promotion_decisions_campaign_id_fkey;
ALTER TABLE promotion_decisions ADD FOREIGN KEY(campaign_id) REFERENCES promotion_campaigns(id);
DELETE FROM promotion_views WHERE kind = 'click';
ALTER TABLE promotion_views DROP CONSTRAINT promotion_views_pkey;
ALTER TABLE promotion_views DROP COLUMN kind;
ALTER TABLE promotion_views ADD PRIMARY KEY(campaign_id, day, visitor_hash);
DROP INDEX promotion_created, promotion_product;
ALTER TABLE promotion_campaigns DROP COLUMN created_at, DROP COLUMN updated_at, DROP COLUMN product_id;
