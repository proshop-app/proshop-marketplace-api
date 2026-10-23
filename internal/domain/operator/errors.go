package operator

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

// Ошибки входа и проверки прав оператора.
var (
	ErrUnauthorized = errs.New(errs.Unauthorized, "operator.unauthorized", "нужен вход оператора")
	ErrForbidden    = errs.New(errs.Forbidden, "operator.forbidden", "нужна роль оператора")
	ErrLimited      = errs.New(errs.TooManyRequests, "operator.rate_limit", "слишком много попыток входа")
)
