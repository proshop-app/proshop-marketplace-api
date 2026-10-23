package event

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

// Ошибки операторского разбора сообщений.
var (
	ErrNotFound = errs.New(errs.NotFound, "event.not_found", "событие не найдено")
	ErrConflict = errs.New(errs.Conflict, "event.conflict", "состояние события изменилось")
	ErrNote     = errs.New(errs.Invalid, "event.note", "заметка должна содержать от 1 до 2000 символов")
	ErrFilter   = errs.New(errs.Invalid, "event.filter", "некорректные фильтры событий")
)
