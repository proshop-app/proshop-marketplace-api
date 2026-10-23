// Package errs — типизированные ошибки с кодами. Код определяет класс ошибки
// (по нему транспорт выбирает ответ), ключ — конкретную ошибку (по нему
// errors.Is отличает одну доменную ошибку от другой того же класса).
package errs

import (
	"errors"
	"fmt"
)

// Code — класс ошибки. Список закрыт: транспорт переводит его в ответ одной таблицей.
type Code string

const (
	// Invalid — входные данные нарушают правило; повтор с теми же данными не поможет.
	Invalid Code = "invalid"
	// NotFound — сущности нет.
	NotFound Code = "not_found"
	// Conflict — состояние изменилось или нарушает уникальность; можно перечитать и повторить.
	Conflict Code = "conflict"
	// Unauthorized — нет или неверны учётные данные.
	Unauthorized Code = "unauthorized"
	// Forbidden — учётная запись не имеет нужной роли.
	Forbidden Code = "forbidden"
	// TooManyRequests — превышен лимит частоты; повтор позже пройдёт.
	TooManyRequests Code = "too_many_requests"
	// Unavailable — внешняя зависимость недоступна; повтор позже может пройти.
	Unavailable Code = "unavailable"
	// Internal — непредвиденная ошибка.
	Internal Code = "internal"
)

// Error — ошибка с кодом, стабильным ключом и сообщением для человека.
type Error struct {
	code    Code
	key     string
	message string
	cause   error
}

// New заводит ошибку. Ключ — стабильный идентификатор вида "контекст.причина".
func New(code Code, key, message string) *Error {
	return &Error{code: code, key: key, message: message}
}

// Code — класс ошибки.
func (e *Error) Code() Code { return e.code }

// Key — стабильный ключ ошибки.
func (e *Error) Key() string { return e.key }

// Message — сообщение для человека.
func (e *Error) Message() string { return e.message }

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.key, e.message, e.cause)
	}

	return fmt.Sprintf("%s: %s", e.key, e.message)
}

// Unwrap отдаёт причину.
func (e *Error) Unwrap() error { return e.cause }

// Is сравнивает ошибки по ключу: копия с другим сообщением или причиной — та же ошибка.
func (e *Error) Is(target error) bool {
	var other *Error
	if !errors.As(target, &other) {
		return false
	}

	return other.key == e.key
}

// WithMessage — та же ошибка с уточнённым сообщением.
func (e *Error) WithMessage(message string) *Error {
	clone := *e
	clone.message = message

	return &clone
}

// Wrap — та же ошибка с причиной.
func (e *Error) Wrap(cause error) *Error {
	clone := *e
	clone.cause = cause

	return &clone
}

// CodeOf — класс ошибки; ошибка без кода считается внутренней.
func CodeOf(err error) Code {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.code
	}

	return Internal
}

// As достаёт типизированную ошибку из цепочки.
func As(err error) (*Error, bool) {
	var typed *Error
	ok := errors.As(err, &typed)

	return typed, ok
}
