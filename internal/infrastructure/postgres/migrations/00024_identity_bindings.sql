-- Identity-hub mapping не содержит контактов или секретов.
-- +goose Up
CREATE TABLE identity_bindings (
    issuer text NOT NULL,
    client_id text NOT NULL,
    subject text NOT NULL,
    local_id text NOT NULL,
    PRIMARY KEY (issuer, client_id, subject),
    UNIQUE (issuer, client_id, local_id)
);

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM identity_bindings) THEN
        RAISE EXCEPTION 'identity_bindings содержит рабочие соответствия; автоматический откат запрещён';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE identity_bindings;
