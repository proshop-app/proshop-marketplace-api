-- +goose Up
ALTER TABLE tenants ADD COLUMN name text NOT NULL DEFAULT '';
-- Продакшн-данных нет: старые строки начинают рассмотрение заново, без переноса решений.
ALTER TABLE participants
 DROP COLUMN status,
 DROP COLUMN enabled,
 ADD COLUMN stage text NOT NULL DEFAULT 'candidate',
 ADD COLUMN enabled boolean NOT NULL DEFAULT false,
 ADD CONSTRAINT participants_stage_check CHECK (stage IN ('candidate', 'connected', 'returned', 'disconnected')),
 ADD CONSTRAINT participants_enabled_stage CHECK (enabled = (stage = 'connected')),
 ADD COLUMN awaiting_review boolean NOT NULL DEFAULT true,
 ADD COLUMN delivery_valid boolean NOT NULL DEFAULT false,
 ADD COLUMN last_decision jsonb,
 DROP COLUMN shop_enabled;
CREATE INDEX participants_queue ON participants (created_at, tenant_id)
 WHERE stage = 'candidate' AND awaiting_review AND delivery_valid;
ALTER TABLE participant_decisions DROP CONSTRAINT participant_decisions_action_check;
ALTER TABLE participant_decisions ADD CONSTRAINT participant_decisions_action_check
 CHECK (action IN ('candidate', 'return', 'enable', 'disable', 'trust'));

-- +goose Down
-- Расширенный словарь действий сохраняется: журнал решений неизменяем.
DROP INDEX participants_queue;
ALTER TABLE participants
 DROP CONSTRAINT participants_stage_check,
 DROP CONSTRAINT participants_enabled_stage,
 DROP COLUMN awaiting_review,
 DROP COLUMN delivery_valid,
 DROP COLUMN last_decision,
 ADD COLUMN shop_enabled boolean NOT NULL DEFAULT false;
UPDATE participants SET stage = CASE WHEN enabled THEN 'connected' ELSE 'onboarding' END;
ALTER TABLE participants ALTER COLUMN stage DROP DEFAULT;
ALTER TABLE participants RENAME COLUMN stage TO status;
ALTER TABLE participants ADD CONSTRAINT participants_status_check CHECK (status IN ('onboarding', 'connected'));
ALTER TABLE tenants DROP COLUMN name;
