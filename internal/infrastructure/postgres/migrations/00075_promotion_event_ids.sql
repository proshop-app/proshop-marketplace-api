-- +goose Up
TRUNCATE promotion_views;
ALTER TABLE promotion_views DROP COLUMN visitor_hash;
ALTER TABLE promotion_views ADD COLUMN event_id uuid NOT NULL;
ALTER TABLE promotion_views ADD PRIMARY KEY (campaign_id, event_id, kind);

-- +goose Down
TRUNCATE promotion_views;
ALTER TABLE promotion_views DROP COLUMN event_id;
ALTER TABLE promotion_views ADD COLUMN visitor_hash text NOT NULL;
ALTER TABLE promotion_views ADD PRIMARY KEY (campaign_id, day, visitor_hash, kind);
