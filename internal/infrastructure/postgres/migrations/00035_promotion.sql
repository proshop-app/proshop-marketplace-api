-- +goose Up
CREATE TABLE promotion_placements (
 code text PRIMARY KEY,
 surface text NOT NULL,
 title text NOT NULL,
 positions integer[] NOT NULL,
 slot_limit integer NOT NULL DEFAULT 1 CHECK (slot_limit >= 1),
 default_price bigint NOT NULL DEFAULT 0 CHECK (default_price >= 0),
 position_prices jsonb NOT NULL DEFAULT '{}',
 active boolean NOT NULL DEFAULT false,
 scope text NOT NULL CHECK (scope IN ('category', 'global')),
 version bigint NOT NULL DEFAULT 1
);
INSERT INTO promotion_placements(code, surface, title, positions, scope) VALUES
 ('category_slot', 'category_slot', 'Спонсорская позиция в категории', '{1,5}', 'category'),
 ('search_slot', 'search_slot', 'Спонсорская позиция в поиске', '{1,5}', 'global'),
 ('home_top_banner', 'home_top_banner', 'Баннер вверху главной', '{1}', 'global'),
 ('home_feed_banner', 'home_feed_banner', 'Баннер в ленте главной', '{1}', 'global'),
 ('category_banner', 'category_banner', 'Баннер категории', '{1}', 'category'),
 ('home_stores', 'home_stores', 'Рекламный ряд магазинов', '{1}', 'global'),
 ('store_card_slot', 'store_card_slot', 'Слот в карточке магазина', '{1}', 'global'),
 ('showcase', 'showcase', 'Магазин недели', '{1}', 'global');
CREATE TABLE promotion_campaigns (
 id uuid PRIMARY KEY,
 tenant_id text NOT NULL,
 kind text NOT NULL CHECK (kind IN ('sponsored_slot', 'banner', 'showcase')),
 placement_code text NOT NULL REFERENCES promotion_placements(code),
 position integer NOT NULL,
 category_id text NOT NULL,
 date_from date NOT NULL CHECK (extract(isodow FROM date_from) = 1),
 date_to date NOT NULL CHECK (date_to >= date_from AND extract(isodow FROM date_to) = 7),
 price bigint NOT NULL CHECK (price > 0),
 payment_amount bigint NOT NULL DEFAULT 0,
 status text NOT NULL CHECK (status IN ('draft','review','approved','rejected','active','finished','stopped')),
 version bigint NOT NULL,
 state jsonb NOT NULL
);
CREATE INDEX promotion_capacity ON promotion_campaigns(placement_code, position, category_id, date_from, date_to)
 WHERE status IN ('review', 'approved', 'active');
CREATE INDEX promotion_tenant ON promotion_campaigns(tenant_id, id);
CREATE TABLE promotion_decisions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 campaign_id uuid NOT NULL REFERENCES promotion_campaigns(id),
 at timestamptz NOT NULL,
 actor text NOT NULL CHECK (actor IN ('operator','tenant','system')),
 action text NOT NULL,
 note text NOT NULL DEFAULT ''
);
CREATE TABLE promotion_counters (
 campaign_id uuid NOT NULL REFERENCES promotion_campaigns(id),
 day date NOT NULL,
 position integer NOT NULL,
 impressions bigint NOT NULL DEFAULT 0,
 clicks bigint NOT NULL DEFAULT 0,
 PRIMARY KEY(campaign_id, day, position)
);
CREATE TABLE promotion_views (
 campaign_id uuid NOT NULL REFERENCES promotion_campaigns(id),
 day date NOT NULL,
 visitor_hash text NOT NULL,
 PRIMARY KEY(campaign_id, day, visitor_hash)
);
-- +goose Down
DROP TABLE promotion_views, promotion_counters, promotion_decisions, promotion_campaigns, promotion_placements;
