-- +goose Up
ALTER TABLE catalog_products ADD COLUMN store_ranked_at timestamptz, ADD COLUMN store_ranked_revision bigint;
UPDATE catalog_products SET store_ranked_at = projected_at, store_ranked_revision = revision;
ALTER TABLE catalog_products ALTER COLUMN store_ranked_at SET NOT NULL,
 ALTER COLUMN store_ranked_revision SET NOT NULL;

CREATE TABLE store_summary_products (
 tenant_id text NOT NULL,
 product_id text COLLATE "C" NOT NULL,
 raw_category text,
 category text,
 price bigint,
 visible boolean NOT NULL,
 name text NOT NULL,
 image text NOT NULL,
 ranked_at timestamptz NOT NULL,
 ranked_revision bigint NOT NULL,
 sales bigint NOT NULL,
 rating_group text NOT NULL,
 rating double precision NOT NULL,
 ratings bigint NOT NULL,
 PRIMARY KEY (tenant_id, product_id)
);
CREATE INDEX store_summary_candidate_price ON store_summary_products (tenant_id, price) WHERE price IS NOT NULL;
CREATE INDEX store_summary_visible_price ON store_summary_products (tenant_id, price) WHERE visible;
CREATE INDEX store_summary_showcase ON store_summary_products
 (tenant_id, sales DESC, (image <> '') DESC, ranked_at DESC, ranked_revision DESC, product_id) WHERE visible;
CREATE INDEX store_summary_rating_group ON store_summary_products (tenant_id, rating_group, product_id) WHERE visible;

CREATE TABLE store_summary_counts (
 tenant_id text NOT NULL,
 kind text NOT NULL,
 category text NOT NULL,
 total integer NOT NULL,
 PRIMARY KEY (tenant_id, category, kind)
);
CREATE INDEX store_summary_empty_counts ON store_summary_counts (tenant_id) WHERE total = 0;
CREATE TABLE store_summary_ratings (
 tenant_id text PRIMARY KEY,
 weight numeric NOT NULL,
 ratings bigint NOT NULL,
 card_version bigint NOT NULL
);

-- Позицию даём перед фиксацией. До этого писатели добавляют дельты
-- независимо, а читатель не видит незавершённую порцию.
CREATE TABLE store_summary_changes (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id text NOT NULL,
 product_id text NOT NULL,
 source_xid xid8 DEFAULT pg_current_xact_id(),
 batch_no bigint,
 scheduled boolean NOT NULL DEFAULT false,
 CHECK ((source_xid IS NULL) <> (batch_no IS NULL))
);
CREATE INDEX store_summary_changes_staged ON store_summary_changes (source_xid, tenant_id)
 WHERE source_xid IS NOT NULL;
CREATE INDEX store_summary_changes_tenant ON store_summary_changes (tenant_id, batch_no, id) WHERE batch_no IS NOT NULL;
CREATE INDEX store_summary_changes_unscheduled ON store_summary_changes (tenant_id, product_id)
 WHERE batch_no IS NOT NULL AND NOT scheduled;
CREATE TABLE store_summary_change_heads (
 tenant_id text PRIMARY KEY,
 batch_no bigint NOT NULL
);
CREATE TABLE store_summary_change_sources (
 source_xid xid8 PRIMARY KEY
);
CREATE TABLE store_summary_change_cursor (
 tenant_id text PRIMARY KEY,
 batch_no bigint NOT NULL,
 id bigint NOT NULL
);

-- +goose StatementBegin
CREATE FUNCTION publish_store_summary_changes() RETURNS trigger
LANGUAGE plpgsql SET enable_seqscan TO off AS $$
BEGIN
 -- Порядок блокировок одинаков для транзакций с несколькими магазинами.
 -- Сводка читает отдельный курсор и никогда не блокирует счётчик записи.
 WITH staged AS MATERIALIZED (
  SELECT DISTINCT tenant_id FROM store_summary_changes WHERE source_xid = NEW.source_xid
 ), heads AS (
  INSERT INTO store_summary_change_heads AS h (tenant_id, batch_no)
  SELECT tenant_id, 1 FROM staged ORDER BY tenant_id
  ON CONFLICT (tenant_id) DO UPDATE SET batch_no = h.batch_no + 1
  RETURNING tenant_id, batch_no
 )
 UPDATE store_summary_changes c SET batch_no = h.batch_no, source_xid = NULL
 FROM heads h WHERE c.source_xid = NEW.source_xid AND c.tenant_id = h.tenant_id;

 DELETE FROM store_summary_change_sources WHERE source_xid = NEW.source_xid;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER store_summary_publish_at_commit AFTER INSERT ON store_summary_change_sources
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION publish_store_summary_changes();

-- +goose StatementBegin
CREATE FUNCTION stage_store_summary_change(shop text, product text) RETURNS void LANGUAGE sql AS $$
 INSERT INTO store_summary_change_sources VALUES (pg_current_xact_id()) ON CONFLICT DO NOTHING;
 INSERT INTO store_summary_changes (tenant_id, product_id) VALUES (shop, product);
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION store_summary_product(p catalog_products, allowed boolean)
RETURNS store_summary_products LANGUAGE sql IMMUTABLE AS $$
 SELECT p.tenant_id, p.product_id,
  CASE WHEN NOT p.deleted AND p.card->'marketplace'->>'published' = 'true'
   THEN COALESCE(p.card->'marketplace'->>'categoryId', '') END,
  CASE WHEN candidate THEN COALESCE(p.card->'publication'->>'categoryId', '') END,
  CASE WHEN candidate THEN p.price END,
  visible,
  CASE WHEN visible THEN COALESCE(p.card->>'name', '') ELSE '' END,
  CASE WHEN visible THEN COALESCE(p.card->>'image', '') ELSE '' END,
  CASE WHEN visible THEN p.store_ranked_at ELSE '-infinity'::timestamptz END,
  CASE WHEN visible THEN p.store_ranked_revision ELSE 0 END,
  CASE WHEN visible THEN p.sales_count ELSE 0 END,
  CASE WHEN NOT visible THEN '' WHEN COALESCE(p.card->>'variantsId', '') = '' THEN 'product:' || p.product_id
   ELSE 'variants:' || (p.card->>'variantsId') END,
  CASE WHEN visible THEN p.average_rating ELSE 0 END,
  CASE WHEN visible THEN p.ratings_count ELSE 0 END
 FROM LATERAL (
  SELECT NOT p.deleted AND p.price IS NOT NULL AND (p.card->'publication'->>'published' = 'true') IS TRUE AS candidate
 ) c, LATERAL (
  SELECT candidate AND (p.card->>'visible' = 'true') IS TRUE AND allowed AS visible
 ) v
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION record_store_summary_product() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 before store_summary_products;
 after store_summary_products;
BEGIN
 IF TG_OP = 'DELETE' THEN
  before := store_summary_product(OLD, true);
  IF before.raw_category IS NOT NULL OR before.price IS NOT NULL THEN
   PERFORM stage_store_summary_change(OLD.tenant_id, OLD.product_id);
  END IF;
  RETURN OLD;
 END IF;
 IF TG_OP = 'UPDATE' THEN
  -- Для сравнения вклада сохраняем порядок, присвоенный при его последнем изменении.
  NEW.store_ranked_at := OLD.store_ranked_at;
  NEW.store_ranked_revision := OLD.store_ranked_revision;
  before := store_summary_product(OLD, true);
 END IF;
 after := store_summary_product(NEW, true);
 IF after IS NOT DISTINCT FROM before THEN
  RETURN NEW;
 END IF;
 NEW.store_ranked_at := NEW.projected_at;
 NEW.store_ranked_revision := NEW.revision;

 IF after.raw_category IS NOT NULL OR after.price IS NOT NULL
  OR before.raw_category IS NOT NULL OR before.price IS NOT NULL THEN
  PERFORM stage_store_summary_change(NEW.tenant_id, NEW.product_id);
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER store_summary_product_changed BEFORE INSERT OR UPDATE OR DELETE ON catalog_products
 FOR EACH ROW EXECUTE FUNCTION record_store_summary_product();

-- +goose StatementBegin
CREATE FUNCTION record_store_summary_verdict() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'UPDATE' AND (OLD.status = 'allowed') = (NEW.status = 'allowed') THEN
  RETURN NULL;
 END IF;
 -- Товар и вердикт могут коммититься независимо. Каждый источник сохраняет
 -- изменение, чтобы сводка могла прочитать их общий актуальный снимок.
 IF TG_OP <> 'INSERT' AND OLD.status = 'allowed' THEN
  PERFORM stage_store_summary_change(OLD.tenant_id, OLD.product_id);
 ELSIF TG_OP <> 'DELETE' AND NEW.status = 'allowed' THEN
  PERFORM stage_store_summary_change(NEW.tenant_id, NEW.product_id);
 END IF;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER store_summary_verdict_changed AFTER INSERT OR UPDATE OR DELETE ON moderation_verdicts
 FOR EACH ROW EXECUTE FUNCTION record_store_summary_verdict();

CREATE TYPE store_summary_delta AS (
 product_id text,
 before store_summary_products,
 after store_summary_products
);

-- +goose StatementBegin
CREATE FUNCTION store_summary_rating(shop text, groups text[], OUT weight numeric, OUT ratings bigint)
LANGUAGE sql STABLE AS $$
 SELECT COALESCE(sum(s.rating::numeric * s.ratings), 0), COALESCE(sum(s.ratings), 0)::bigint
 FROM unnest(groups) g(id)
 CROSS JOIN LATERAL (
  SELECT rating, ratings FROM store_summary_products
  WHERE tenant_id = shop AND visible AND rating_group = g.id ORDER BY product_id LIMIT 1
 ) s
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION store_summary_change_keys(shop text, after_batch_no bigint, after_id bigint, batch_size integer)
RETURNS TABLE (batch_no bigint, id bigint) LANGUAGE sql STABLE AS $$
 SELECT c.batch_no, c.id FROM store_summary_changes c
 WHERE c.tenant_id = shop AND c.batch_no IS NOT NULL AND (c.batch_no, c.id) > (after_batch_no, after_id)
 ORDER BY c.batch_no, c.id LIMIT batch_size
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION take_store_summary_changes(shop text, batch_size integer) RETURNS text[]
-- Чужой снимок может задержать уборку удалённых строк, поэтому обе
-- границы порции используем как условия индексного чтения.
LANGUAGE sql SET enable_seqscan TO off AS $$
 WITH cursor AS MATERIALIZED (
  SELECT COALESCE(c.batch_no, 0) AS batch_no, COALESCE(c.id, 0) AS id
  FROM (VALUES (shop)) s(tenant_id) LEFT JOIN store_summary_change_cursor c USING (tenant_id)
 ), last AS MATERIALIZED (
  SELECT k.batch_no, k.id FROM cursor c
  CROSS JOIN LATERAL store_summary_change_keys(shop, c.batch_no, c.id, batch_size) k
  ORDER BY k.batch_no DESC, k.id DESC LIMIT 1
 ), removed AS (
  DELETE FROM store_summary_changes c
  WHERE c.tenant_id = shop AND c.batch_no IS NOT NULL
   AND (c.batch_no, c.id) > (SELECT batch_no, id FROM cursor)
   AND (c.batch_no, c.id) <= (SELECT batch_no, id FROM last)
  RETURNING c.product_id
 ), advance AS (
  INSERT INTO store_summary_change_cursor (tenant_id, batch_no, id)
  SELECT shop, batch_no, id FROM last
  ON CONFLICT (tenant_id) DO UPDATE SET batch_no = EXCLUDED.batch_no, id = EXCLUDED.id
 )
 SELECT ARRAY(SELECT product_id FROM removed)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION apply_store_summary_counts(shop text, deltas store_summary_delta[]) RETURNS void LANGUAGE sql AS $$
 WITH changes AS (
  SELECT c.kind, c.category, sum(s.direction) AS change
  FROM unnest(deltas) d
  CROSS JOIN LATERAL (VALUES (d.before, -1), (d.after, 1)) s(item, direction)
  CROSS JOIN LATERAL (VALUES
   ('raw', (s.item).raw_category),
   ('raw_total', CASE WHEN (s.item).raw_category IS NOT NULL THEN '' END),
   ('candidate', (s.item).category),
   ('candidate_total', CASE WHEN (s.item).price IS NOT NULL THEN '' END),
   ('visible', CASE WHEN (s.item).visible THEN (s.item).category END),
   ('visible_total', CASE WHEN (s.item).visible THEN '' END)
  ) c(kind, category)
  WHERE c.category IS NOT NULL GROUP BY c.kind, c.category HAVING sum(s.direction) <> 0
 )
 INSERT INTO store_summary_counts (tenant_id, kind, category, total)
  SELECT shop, kind, category, change FROM changes
  ON CONFLICT (tenant_id, kind, category) DO UPDATE SET total = store_summary_counts.total + EXCLUDED.total;

 DELETE FROM store_summary_counts WHERE tenant_id = shop AND total = 0;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION save_store_summary_products(shop text, deltas store_summary_delta[]) RETURNS void LANGUAGE sql AS $$
 DELETE FROM store_summary_products p
  USING unnest(deltas) d
  WHERE p.tenant_id = shop AND p.product_id = d.product_id
   AND (d.after).raw_category IS NULL AND (d.after).price IS NULL;

 INSERT INTO store_summary_products SELECT (d.after).* FROM unnest(deltas) d
  WHERE (d.after).raw_category IS NOT NULL OR (d.after).price IS NOT NULL
  ON CONFLICT (tenant_id, product_id) DO UPDATE
  SET raw_category = EXCLUDED.raw_category,
   category = EXCLUDED.category,
   price = EXCLUDED.price,
   visible = EXCLUDED.visible,
   name = EXCLUDED.name,
   image = EXCLUDED.image,
   ranked_at = EXCLUDED.ranked_at,
   ranked_revision = EXCLUDED.ranked_revision,
   sales = EXCLUDED.sales,
   rating_group = EXCLUDED.rating_group,
   rating = EXCLUDED.rating,
   ratings = EXCLUDED.ratings;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION apply_store_summary_changes(shop text, batch_size integer) RETURNS integer LANGUAGE plpgsql AS $$
DECLARE
 deltas store_summary_delta[];
 products text[] := take_store_summary_changes(shop, batch_size);
 consumed integer := cardinality(products);
 groups text[];
 old_weight numeric := 0;
 weight_delta numeric := 0;
 old_ratings bigint := 0;
 ratings_delta bigint := 0;
 card_changed boolean;
BEGIN
 SELECT array_agg(ROW(k.product_id, old.value, new.value)::store_summary_delta)
 INTO deltas FROM (SELECT DISTINCT unnest(products) AS product_id) k
 LEFT JOIN LATERAL (
  SELECT p AS value FROM store_summary_products p
  WHERE p.tenant_id = shop AND p.product_id = k.product_id LIMIT 1
 ) old ON true
 LEFT JOIN LATERAL (
  SELECT store_summary_product(p, EXISTS (
   SELECT 1 FROM moderation_verdicts v
   WHERE v.tenant_id = shop AND v.product_id = k.product_id AND v.status = 'allowed'
  )) AS value
  FROM catalog_products p WHERE p.tenant_id = shop AND p.product_id = k.product_id LIMIT 1
 ) new ON true
 WHERE old.value IS DISTINCT FROM new.value;

 IF deltas IS NULL THEN
  RETURN consumed;
 END IF;

 PERFORM apply_store_summary_counts(shop, deltas);

 SELECT array_agg(DISTINCT g.id) INTO groups
 FROM unnest(deltas) d
 CROSS JOIN LATERAL (VALUES
  (CASE WHEN (d.before).visible THEN (d.before).rating_group END),
  (CASE WHEN (d.after).visible THEN (d.after).rating_group END)
 ) g(id)
 WHERE g.id IS NOT NULL AND ROW((d.before).visible, (d.before).rating_group, (d.before).rating, (d.before).ratings)
  IS DISTINCT FROM ROW((d.after).visible, (d.after).rating_group, (d.after).rating, (d.after).ratings);
 IF groups IS NOT NULL THEN
  SELECT r.weight, r.ratings INTO old_weight, old_ratings FROM store_summary_rating(shop, groups) r;
 END IF;

 PERFORM save_store_summary_products(shop, deltas);

 IF groups IS NOT NULL THEN
  SELECT r.weight - old_weight, r.ratings - old_ratings INTO weight_delta, ratings_delta
  FROM store_summary_rating(shop, groups) r;
 END IF;

 SELECT EXISTS (
  SELECT 1 FROM unnest(deltas) d
  WHERE ROW(CASE WHEN (d.before).visible THEN (d.before).name END,
   CASE WHEN (d.before).visible THEN (d.before).image END, CASE WHEN (d.before).visible THEN (d.before).category END)
  IS DISTINCT FROM ROW(CASE WHEN (d.after).visible THEN (d.after).name END,
   CASE WHEN (d.after).visible THEN (d.after).image END, CASE WHEN (d.after).visible THEN (d.after).category END)
 ) INTO card_changed;

 IF card_changed OR weight_delta <> 0 OR ratings_delta <> 0 THEN
  INSERT INTO store_summary_ratings (tenant_id, weight, ratings, card_version)
  VALUES (shop, weight_delta, ratings_delta, card_changed::integer)
  ON CONFLICT (tenant_id) DO UPDATE
  SET weight = store_summary_ratings.weight + EXCLUDED.weight,
   ratings = store_summary_ratings.ratings + EXCLUDED.ratings,
   card_version = store_summary_ratings.card_version + EXCLUDED.card_version;
 END IF;

 RETURN consumed;
END;
$$;
-- +goose StatementEnd

INSERT INTO store_summary_change_sources VALUES (pg_current_xact_id());
INSERT INTO store_summary_changes (tenant_id, product_id)
 SELECT tenant_id, product_id FROM catalog_products
 WHERE NOT deleted AND (card->'marketplace'->>'published' = 'true'
  OR (price IS NOT NULL AND card->'publication'->>'published' = 'true'));

INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at)
 SELECT 'participant.store-card.refresh', tenant_id, 'participant.store-card.refresh:' || tenant_id,
 '{}'::jsonb, now(), now() FROM participants
 ON CONFLICT (dedup_key) WHERE attempts = 0 DO UPDATE SET next_at = LEAST(outbox.next_at, EXCLUDED.next_at);

DROP INDEX catalog_products_store_summary;

-- +goose Down
CREATE INDEX catalog_products_store_summary ON catalog_products (
 tenant_id, (CASE WHEN COALESCE(card->>'variantsId', '') = '' THEN 'product:' || product_id
  ELSE 'variants:' || (card->>'variantsId') END) COLLATE "C", product_id COLLATE "C"
) WHERE NOT deleted AND (COALESCE(card->'marketplace'->>'published', 'false')::boolean
 OR COALESCE(card->'publication'->>'published', 'false')::boolean);
DROP TRIGGER store_summary_product_changed ON catalog_products;
DROP TRIGGER store_summary_verdict_changed ON moderation_verdicts;
DROP FUNCTION record_store_summary_product();
DROP FUNCTION record_store_summary_verdict();
DROP FUNCTION apply_store_summary_changes(text, integer);
DROP FUNCTION save_store_summary_products(text, store_summary_delta[]);
DROP FUNCTION apply_store_summary_counts(text, store_summary_delta[]);
DROP FUNCTION take_store_summary_changes(text, integer);
DROP FUNCTION store_summary_change_keys(text, bigint, bigint, integer);
DROP FUNCTION stage_store_summary_change(text, text);
DROP TRIGGER store_summary_publish_at_commit ON store_summary_change_sources;
DROP FUNCTION publish_store_summary_changes();
DROP FUNCTION store_summary_rating(text, text[]);
DROP TYPE store_summary_delta;
DROP FUNCTION store_summary_product(catalog_products, boolean);
DROP TABLE store_summary_change_heads, store_summary_change_sources, store_summary_change_cursor,
 store_summary_changes, store_summary_counts, store_summary_ratings, store_summary_products;
ALTER TABLE catalog_products DROP COLUMN store_ranked_at, DROP COLUMN store_ranked_revision;
