-- +goose Up
CREATE TABLE moderation_verdicts (
 tenant_id text NOT NULL,
 product_id text NOT NULL,
 revision bigint NOT NULL,
 rules_revision bigint NOT NULL,
 taxonomy_version bigint NOT NULL,
 status text NOT NULL CHECK (status IN ('allowed', 'pending', 'rejected')),
 reasons jsonb NOT NULL,
 decided_by text NOT NULL CHECK (decided_by IN ('rule', 'operator')),
 manual_reject boolean NOT NULL DEFAULT false,
 version bigint NOT NULL,
 updated_at timestamptz NOT NULL,
 PRIMARY KEY (tenant_id, product_id)
);
CREATE INDEX moderation_verdict_status ON moderation_verdicts (status, tenant_id);
CREATE TABLE moderation_cases (
 id uuid PRIMARY KEY,
 tenant_id text NOT NULL,
 product_id text NOT NULL,
 revision bigint NOT NULL,
 source text NOT NULL CHECK (source IN ('rule', 'operator')),
 status text NOT NULL CHECK (status IN ('open', 'allowed', 'rejected', 'superseded')),
 reasons jsonb NOT NULL,
 note text NOT NULL,
 opened_at timestamptz NOT NULL,
 decided_at timestamptz,
 version bigint NOT NULL
);
CREATE UNIQUE INDEX moderation_case_open ON moderation_cases (tenant_id, product_id) WHERE status = 'open';
CREATE INDEX moderation_case_queue ON moderation_cases (status, opened_at);
CREATE TABLE moderation_decisions (
 id uuid PRIMARY KEY,
 case_id uuid NOT NULL REFERENCES moderation_cases(id),
 operator_id text NOT NULL,
 operator_name text NOT NULL,
 action text NOT NULL CHECK (action IN ('allow', 'reject')),
 reason text NOT NULL,
 note text NOT NULL CHECK (char_length(btrim(note)) BETWEEN 1 AND 2000),
 revision bigint NOT NULL,
 at timestamptz NOT NULL
);
CREATE TABLE moderation_stop_phrases (
 key text PRIMARY KEY,
 text text NOT NULL CHECK (char_length(text) BETWEEN 2 AND 120),
 operator_id text NOT NULL,
 operator_name text NOT NULL,
 created_at timestamptz NOT NULL
);
CREATE TABLE moderation_reasons (
 code text PRIMARY KEY CHECK (code ~ '^[a-z][a-z0-9_]{0,63}$'),
 text text NOT NULL CHECK (char_length(text) BETWEEN 1 AND 200)
);
CREATE TABLE moderation_rules (
 id integer PRIMARY KEY CHECK (id = 1),
 revision bigint NOT NULL
);
INSERT INTO moderation_rules (id, revision) VALUES (1, 1);
ALTER TABLE participants ADD COLUMN trust text NOT NULL DEFAULT 'probation'
 CHECK (trust IN ('probation', 'trusted'));
ALTER TABLE participants ADD COLUMN approved_offers integer NOT NULL DEFAULT 0 CHECK (approved_offers >= 0);
CREATE TABLE participant_decisions (
 id uuid PRIMARY KEY,
 tenant_id text NOT NULL REFERENCES participants(tenant_id),
 operator_id text NOT NULL,
 operator_name text NOT NULL,
 action text NOT NULL CHECK (action IN ('enable', 'disable', 'trust')),
 value text NOT NULL,
 reason text NOT NULL,
 note text NOT NULL,
 at timestamptz NOT NULL,
 CHECK (action <> 'trust' OR value IN ('probation', 'trusted'))
);
CREATE INDEX participant_decisions_tenant ON participant_decisions (tenant_id, at DESC);
-- +goose StatementBegin
CREATE FUNCTION moderation_journal_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'журнал решений допускает только добавление';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER moderation_decisions_immutable BEFORE UPDATE OR DELETE ON moderation_decisions
 FOR EACH ROW EXECUTE FUNCTION moderation_journal_immutable();
CREATE TRIGGER participant_decisions_immutable BEFORE UPDATE OR DELETE ON participant_decisions
 FOR EACH ROW EXECUTE FUNCTION moderation_journal_immutable();

-- +goose Down
DROP TABLE participant_decisions;
ALTER TABLE participants DROP COLUMN approved_offers, DROP COLUMN trust;
DROP TABLE moderation_decisions;
DROP FUNCTION moderation_journal_immutable();
DROP TABLE moderation_cases, moderation_verdicts, moderation_stop_phrases, moderation_reasons, moderation_rules;
