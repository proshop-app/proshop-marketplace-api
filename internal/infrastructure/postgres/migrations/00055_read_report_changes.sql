-- +goose Up
-- Версии записываются по магазину, а не в общую строку на каждый товар.
CREATE TABLE read_report_changes (
 tenant_id text PRIMARY KEY,
 version bigint NOT NULL DEFAULT 1
);
CREATE TABLE read_reports (
 name text PRIMARY KEY,
 version bigint NOT NULL,
 payload jsonb NOT NULL
);

-- +goose StatementBegin
CREATE FUNCTION mark_read_report_changes() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO read_report_changes (tenant_id, version)
 SELECT DISTINCT tenant_id, 1 FROM changed_rows ORDER BY tenant_id
 ON CONFLICT (tenant_id) DO UPDATE SET version = read_report_changes.version + 1;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION update_read_report_changes() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 -- Heartbeat настроек не меняет версию агрегата и не требует пересчёта отчёта.
 IF TG_TABLE_NAME IN ('catalog_products', 'moderation_verdicts') THEN
  INSERT INTO read_report_changes (tenant_id, version)
  SELECT DISTINCT tenant_id, 1 FROM (
   SELECT tenant_id, product_id, version FROM new_rows
   EXCEPT SELECT tenant_id, product_id, version FROM old_rows
  ) changed ORDER BY tenant_id
  ON CONFLICT (tenant_id) DO UPDATE SET version = read_report_changes.version + 1;
 ELSE
  INSERT INTO read_report_changes (tenant_id, version)
  SELECT DISTINCT tenant_id, 1 FROM (
   SELECT tenant_id, version FROM new_rows
   EXCEPT SELECT tenant_id, version FROM old_rows
  ) changed ORDER BY tenant_id
  ON CONFLICT (tenant_id) DO UPDATE SET version = read_report_changes.version + 1;
 END IF;
 RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE source text;
BEGIN
 FOREACH source IN ARRAY ARRAY['catalog_products', 'participants', 'tenants', 'moderation_verdicts'] LOOP
  EXECUTE format('CREATE TRIGGER read_report_insert AFTER INSERT ON %I '
   'REFERENCING NEW TABLE AS changed_rows FOR EACH STATEMENT EXECUTE FUNCTION mark_read_report_changes()', source);
  EXECUTE format('CREATE TRIGGER read_report_delete AFTER DELETE ON %I '
   'REFERENCING OLD TABLE AS changed_rows FOR EACH STATEMENT EXECUTE FUNCTION mark_read_report_changes()', source);
  EXECUTE format('CREATE TRIGGER read_report_update AFTER UPDATE ON %I '
   'REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows '
   'FOR EACH STATEMENT EXECUTE FUNCTION update_read_report_changes()', source);
 END LOOP;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE source text;
BEGIN
 FOREACH source IN ARRAY ARRAY['catalog_products', 'participants', 'tenants', 'moderation_verdicts'] LOOP
  EXECUTE format('DROP TRIGGER read_report_insert ON %I', source);
  EXECUTE format('DROP TRIGGER read_report_delete ON %I', source);
  EXECUTE format('DROP TRIGGER read_report_update ON %I', source);
 END LOOP;
END;
$$;
-- +goose StatementEnd
DROP FUNCTION update_read_report_changes();
DROP FUNCTION mark_read_report_changes();
DROP TABLE read_reports;
DROP TABLE read_report_changes;
