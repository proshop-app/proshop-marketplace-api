-- +goose Up
CREATE TABLE outbox_lane_pauses (
    lane text PRIMARY KEY,
    resume_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE outbox_lane_pauses;
