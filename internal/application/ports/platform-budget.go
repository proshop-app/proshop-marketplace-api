package ports

import (
	"context"
	"time"
)

type platformBudgetKey struct{}

// WithPlatformCallBudget передаёт бюджет вызова магазина данными, а не сроком контекста.
// Срок вызову ставит только ограждение: истечение своего срока оно засчитывает магазину,
// а истечение срока вызывающего — нет. Свой context.WithTimeout вокруг вызова порта
// истекал бы раньше ограждения, и зависший магазин выглядел бы отменой.
func WithPlatformCallBudget(ctx context.Context, budget time.Duration) context.Context {
	return context.WithValue(ctx, platformBudgetKey{}, budget)
}

// PlatformCallBudget — бюджет, заданный вызывающим; false — действует бюджет ограждения.
func PlatformCallBudget(ctx context.Context) (time.Duration, bool) {
	budget, ok := ctx.Value(platformBudgetKey{}).(time.Duration)
	return budget, ok
}
