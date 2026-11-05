-- +goose Up
ALTER TABLE moderation_cases ADD COLUMN card jsonb NULL;
ALTER TABLE moderation_decisions ADD COLUMN reason_message text NOT NULL DEFAULT '';
ALTER TABLE participant_decisions ADD COLUMN reason_message text NOT NULL DEFAULT '';
ALTER TABLE participant_decisions ADD COLUMN actor text NOT NULL DEFAULT 'operator'
 CHECK (actor IN ('operator', 'system'));

-- +goose Down
ALTER TABLE participant_decisions DROP COLUMN actor, DROP COLUMN reason_message;
ALTER TABLE moderation_decisions DROP COLUMN reason_message;
ALTER TABLE moderation_cases DROP COLUMN card;
