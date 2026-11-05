package moderation

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

// Ошибки сохраняют ключи для единого перевода транспортом.
var (
	ErrInvalid      = errs.New(errs.Invalid, "moderation.invalid", "некорректные данные модерации")
	ErrNotFound     = errs.New(errs.NotFound, "moderation.not_found", "запись модерации не найдена")
	ErrConflict     = errs.New(errs.Conflict, "moderation.verdict_conflict", "вердикт изменён параллельно")
	ErrCaseConflict = errs.New(errs.Conflict, "moderation.case_conflict", "дело изменено, перечитайте его")
	ErrCaseStale    = errs.New(errs.Conflict, "moderation.case_stale", "ревизия дела устарела")
	ErrPhraseExists = errs.New(errs.Conflict, "moderation.phrase_exists", "стоп-фраза уже существует")
)
