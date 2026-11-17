package ports

import "context"

// PlatformCallClass выбирается сценарием, независимо от имени операции сервера.
type PlatformCallClass uint8

// Классы допуска общие для вызывающих сценариев и обёрток порта.
const (
	PlatformRead PlatformCallClass = iota
	PlatformCheckout
)

type platformClassKey struct{}

// WithPlatformCallClass передаёт локальный класс допуска через обёртки порта, без изменения HTTP-договора.
func WithPlatformCallClass(ctx context.Context, class PlatformCallClass) context.Context {
	return context.WithValue(ctx, platformClassKey{}, class)
}

// PlatformClassFromContext по умолчанию оставляет неразмеченный вызов в пуле чтений.
func PlatformClassFromContext(ctx context.Context) PlatformCallClass {
	class, _ := ctx.Value(platformClassKey{}).(PlatformCallClass)
	return class
}
