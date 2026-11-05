-- +goose Up
ALTER TABLE moderation_verdicts
 ADD COLUMN operator_id text NOT NULL DEFAULT '',
 ADD COLUMN operator_name text NOT NULL DEFAULT '',
 ADD COLUMN decision jsonb;
ALTER TABLE participant_decisions ADD COLUMN previous_value text NOT NULL DEFAULT '';

CREATE TABLE moderation_verdict_log (
 id bigserial PRIMARY KEY,
 tenant_id text NOT NULL,
 product_id text NOT NULL,
 revision bigint NOT NULL,
 status text NOT NULL,
 reasons jsonb NOT NULL,
 decided_by text NOT NULL,
 operator_id text NOT NULL,
 operator_name text NOT NULL,
 decision jsonb,
 rules_revision bigint NOT NULL,
 taxonomy_version bigint NOT NULL,
 manual_reject boolean NOT NULL,
 version bigint NOT NULL,
 at timestamptz NOT NULL
);
CREATE INDEX moderation_verdict_log_offer ON moderation_verdict_log (tenant_id, product_id, at DESC, id DESC);
CREATE TRIGGER moderation_verdict_log_immutable BEFORE UPDATE OR DELETE ON moderation_verdict_log
 FOR EACH ROW EXECUTE FUNCTION moderation_journal_immutable();

-- Триггер сохраняет историю в транзакции любого пути изменения вердикта.
-- +goose StatementBegin
CREATE FUNCTION moderation_verdict_record() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'INSERT' OR NEW IS DISTINCT FROM OLD THEN
  INSERT INTO moderation_verdict_log (
   tenant_id, product_id, revision, status, reasons, decided_by,
   operator_id, operator_name, decision, rules_revision, taxonomy_version,
   manual_reject, version, at
  ) VALUES (
   NEW.tenant_id, NEW.product_id, NEW.revision, NEW.status, NEW.reasons, NEW.decided_by,
   NEW.operator_id, NEW.operator_name, NEW.decision, NEW.rules_revision, NEW.taxonomy_version,
   NEW.manual_reject, NEW.version, NEW.updated_at
  );
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER moderation_verdict_record AFTER INSERT OR UPDATE ON moderation_verdicts
 FOR EACH ROW EXECUTE FUNCTION moderation_verdict_record();

-- +goose Down
DROP TRIGGER moderation_verdict_record ON moderation_verdicts;
DROP FUNCTION moderation_verdict_record();
DROP TABLE moderation_verdict_log;
ALTER TABLE participant_decisions DROP COLUMN previous_value;
ALTER TABLE moderation_verdicts DROP COLUMN decision, DROP COLUMN operator_name, DROP COLUMN operator_id;
