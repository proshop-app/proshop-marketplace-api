package ports

import (
	"context"
	"time"
)

// DatabaseBudget — срок SQL и всей операции хранения, включая ожидание соединения.
type DatabaseBudget struct {
	Statement   time.Duration
	Transaction time.Duration
}

// DatabaseBudgets — классы нагрузки без привязки приложения к драйверу.
type DatabaseBudgets struct {
	Read       DatabaseBudget
	Checkout   DatabaseBudget
	Ingestion  DatabaseBudget
	Background DatabaseBudget
}

type databaseBudgetKey struct{}

// WithDatabaseBudget задаёт класс работы, сохраняя отмену и срок вызывающего.
func WithDatabaseBudget(ctx context.Context, budget DatabaseBudget) context.Context {
	return context.WithValue(ctx, databaseBudgetKey{}, budget)
}

// DatabaseBudgetFrom возвращает явно выбранный класс либо бюджет хранилища по умолчанию.
func DatabaseBudgetFrom(ctx context.Context, fallback DatabaseBudget) DatabaseBudget {
	budget, ok := ctx.Value(databaseBudgetKey{}).(DatabaseBudget)
	if !ok || budget.Statement <= 0 || budget.Transaction <= 0 {
		return fallback
	}
	return budget
}
