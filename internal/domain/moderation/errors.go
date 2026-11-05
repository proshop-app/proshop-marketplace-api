package moderation

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

// Ошибки сохраняют ключи для единого перевода транспортом.
var (
	ErrKeyReused = errs.New(
		errs.Conflict,
		"moderation.idempotency_key_reused",
		"ключ использован для другого запроса",
	)
	ErrReasonExists  = errs.New(errs.Conflict, "moderation.reason_exists", "код причины уже существует")
	ErrReasonUnknown = errs.New(errs.Invalid, "moderation.reason_unknown", "неизвестный код причины")
	ErrInvalid       = errs.New(errs.Invalid, "moderation.invalid", "некорректные данные модерации")
	ErrNotFound      = errs.New(errs.NotFound, "moderation.not_found", "запись модерации не найдена")
	ErrConflict      = errs.New(errs.Conflict, "moderation.verdict_conflict", "вердикт изменён параллельно")
	ErrCaseConflict  = errs.New(errs.Conflict, "moderation.case_conflict", "дело изменено, перечитайте его")
	ErrCaseStale     = errs.New(errs.Conflict, "moderation.case_stale", "ревизия дела устарела")
	ErrPhraseExists  = errs.New(errs.Conflict, "moderation.stop_phrase_exists", "стоп-фраза уже существует")
)
