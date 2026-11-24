package ports

import (
	"context"
	"time"
)

type platformBudgetKey struct{}

type platformDeadlineKey struct{}

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

// WithPlatformCallDeadline передаёт крайний момент (часы площадки), после которого вызов магазина
// бесполезен: ограждение после получения места даёт вызову не больше остатка до него, а без остатка
// сервер не вызывает. Заголовок срока тогда не обещает серверу времени, которого у площадки нет.
func WithPlatformCallDeadline(ctx context.Context, deadline time.Time) context.Context {
	return context.WithValue(ctx, platformDeadlineKey{}, deadline)
}

// PlatformCallDeadline — крайний момент вызывающего; false — его нет.
func PlatformCallDeadline(ctx context.Context) (time.Time, bool) {
	deadline, ok := ctx.Value(platformDeadlineKey{}).(time.Time)
	return deadline, ok
}
