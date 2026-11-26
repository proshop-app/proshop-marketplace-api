// Package requestid передаёт идентификатор запроса через контекст.
package requestid

import "context"

type contextKey struct{}

// With сохраняет идентификатор запроса.
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// From возвращает идентификатор запроса, если он задан.
func From(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}
