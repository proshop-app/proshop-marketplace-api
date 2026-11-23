-- +goose Up
-- Номер покупки для покупателя выдаёт последовательность базы в самой вставке: nextval атомарен
-- между параллельными оформлениями и не держит блокировку до конца транзакции, поэтому гонки
-- за номер нет, а лишнего обращения к базе — тоже. GENERATED ALWAYS запрещает записать номер
-- из приложения или переписать его обновлением; уникальность — гарантия поверх последовательности.
-- Пропуски после откатов и повторов по ключу идемпотентности допустимы: номер не счётчик заказов.
-- Существующие покупки получают номера при добавлении колонки.
ALTER TABLE checkouts ADD COLUMN number bigint GENERATED ALWAYS AS IDENTITY (START WITH 10000000);
ALTER TABLE checkouts ADD CONSTRAINT checkouts_number_key UNIQUE (number);

-- +goose Down
ALTER TABLE checkouts DROP COLUMN number;
