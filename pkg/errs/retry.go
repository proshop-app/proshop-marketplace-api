package errs

import (
	"context"
	"errors"
)

// RetryKind определяет, может ли то же намерение выполниться без исправления оператором.
type RetryKind int

// Классы повтора: неизвестная причина, временный сбой или постоянный отказ.
const (
	RetryUnknown RetryKind = iota
	RetryTransient
	RetryPermanent
)

// RetryKindOf не угадывает сетевую ошибку по тексту: адаптер обязан задать Unavailable.
func RetryKindOf(err error) RetryKind {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return RetryTransient
	}
	switch CodeOf(err) {
	case Unavailable, Conflict, TooManyRequests:
		return RetryTransient
	case Invalid, NotFound, Unauthorized, Forbidden:
		return RetryPermanent
	default:
		return RetryUnknown
	}
}
