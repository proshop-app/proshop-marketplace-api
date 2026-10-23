package event

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

// Ошибки операторского разбора сообщений.
var (
	ErrKeyReused = errs.New(errs.Conflict, "event.idempotency_key_reused", "ключ использован для другого запроса")
	ErrNotFound  = errs.New(errs.NotFound, "event.not_found", "событие не найдено")
	ErrConflict  = errs.New(errs.Conflict, "event.conflict", "состояние события изменилось")
	ErrNote      = errs.New(errs.Invalid, "event.note", "заметка должна содержать от 1 до 2000 символов")
	ErrFilter    = errs.New(errs.Invalid, "event.filter", "некорректные фильтры событий")
)
