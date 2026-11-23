package ports

import "context"

// DatabaseClass — класс нагрузки на базу. У каждого класса свой пул соединений и свои
// сроки SQL: волна приёма событий и фоновая работа не занимают соединения запросов
// покупателей. Приложение выбирает класс, не зная о драйвере и пулах.
type DatabaseClass int

// Классы нагрузки. Работа без явного класса относится к фону.
const (
	// DatabaseBackground — outbox, исполнитель оформления, фоновые циклы, операторские маршруты.
	DatabaseBackground DatabaseClass = iota + 1
	// DatabaseRead — чтение покупателя по HTTP.
	DatabaseRead
	// DatabaseCheckout — запись по HTTP: корзина, оформление, вход, избранное, API сервера.
	DatabaseCheckout
	// DatabaseIngestion — приём событий Kafka.
	DatabaseIngestion
)

type databaseClassKey struct{}

// WithDatabaseClass задаёт класс работы, сохраняя отмену и срок вызывающего.
func WithDatabaseClass(ctx context.Context, class DatabaseClass) context.Context {
	return context.WithValue(ctx, databaseClassKey{}, class)
}

// DatabaseClassFrom возвращает явно выбранный класс либо фон.
func DatabaseClassFrom(ctx context.Context) DatabaseClass {
	class, ok := ctx.Value(databaseClassKey{}).(DatabaseClass)
	if ok && class >= DatabaseBackground && class <= DatabaseIngestion {
		return class
	}
	return DatabaseBackground
}
