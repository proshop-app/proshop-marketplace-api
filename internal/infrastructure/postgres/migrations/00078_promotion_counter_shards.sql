-- +goose Up
TRUNCATE promotion_views, promotion_counters;

ALTER TABLE promotion_counters
    ADD COLUMN shard smallint NOT NULL CHECK (shard >= 0 AND shard < 64),
    DROP CONSTRAINT promotion_counters_pkey,
    ADD PRIMARY KEY (campaign_id, day, position, shard);

-- +goose Down
WITH totals AS (
    SELECT campaign_id, day, position, min(shard) AS first_shard,
           sum(impressions) AS impressions, sum(clicks) AS clicks
    FROM promotion_counters
    GROUP BY campaign_id, day, position
)
UPDATE promotion_counters c
SET impressions = totals.impressions,
    clicks = totals.clicks
FROM totals
WHERE (c.campaign_id, c.day, c.position, c.shard) =
      (totals.campaign_id, totals.day, totals.position, totals.first_shard);

DELETE FROM promotion_counters c
WHERE EXISTS (
    SELECT 1 FROM promotion_counters lower_shard
    WHERE (lower_shard.campaign_id, lower_shard.day, lower_shard.position) = (c.campaign_id, c.day, c.position)
      AND lower_shard.shard < c.shard
);

ALTER TABLE promotion_counters
    DROP CONSTRAINT promotion_counters_pkey,
    DROP COLUMN shard,
    ADD PRIMARY KEY (campaign_id, day, position);
