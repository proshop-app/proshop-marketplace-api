-- Курсор кабинета упорядочен по UUID покупки, а не по времени создания.
-- +goose Up
CREATE INDEX checkouts_buyer_cursor_idx ON checkouts (buyer_id, id);

-- +goose Down
DROP INDEX checkouts_buyer_cursor_idx;
