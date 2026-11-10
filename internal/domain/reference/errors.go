package reference

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

// Ошибки реестра; ключи стабильны для потребителей.
var (
	// ErrInvalid — данные записи или варианта нарушают правило словаря.
	ErrInvalid = errs.New(errs.Invalid, "reference.invalid", "некорректные данные реестра")
	// ErrLimit — запись выходит за лимит словаря: записей, вариантов, длины или слов.
	ErrLimit = errs.New(errs.Invalid, "reference.limit_exceeded", "превышен лимит словаря")
	// ErrTransition — статус записи не допускает такого перехода.
	ErrTransition = errs.New(errs.Invalid, "reference.status_transition", "недопустимый переход статуса записи")
	// ErrCanonicalAlias — вариант канонического имени удаляется только переименованием.
	ErrCanonicalAlias = errs.New(
		errs.Invalid,
		"reference.canonical_alias",
		"вариант канонического имени нельзя удалить: сначала переименуйте запись",
	)
	// ErrMergeTarget — объединять можно только подтверждённую запись в другую подтверждённую.
	ErrMergeTarget = errs.New(
		errs.Invalid,
		"reference.merge_target",
		"объединяются только подтверждённые записи, и не сами в себя",
	)
	// ErrDictionaryNotFound — словаря нет.
	ErrDictionaryNotFound = errs.New(errs.NotFound, "reference.dictionary_not_found", "словарь не найден")
	// ErrEntryNotFound — записи нет в словаре.
	ErrEntryNotFound = errs.New(errs.NotFound, "reference.entry_not_found", "запись реестра не найдена")
	// ErrAliasNotFound — варианта нет в словаре.
	ErrAliasNotFound = errs.New(errs.NotFound, "reference.alias_not_found", "вариант написания не найден")
	// ErrKeyTaken — ключ написания принадлежит другой записи словаря: бренды всегда разные.
	ErrKeyTaken = errs.New(errs.Conflict, "reference.key_taken", "написание уже принадлежит другой записи словаря")
	// ErrEntryChanged — запись изменилась после прочтения: ревизия If-Match или версия расходятся.
	ErrEntryChanged = errs.New(errs.Conflict, "reference.entry_changed", "запись изменена, перечитайте её")
	// ErrDictionaryChanged — решение основано на прежнем составе словаря.
	ErrDictionaryChanged = errs.New(
		errs.Conflict,
		"reference.dictionary_changed",
		"словарь изменён, перечитайте кандидатов и выберите записи заново",
	)
	// ErrSelectionChanged — выбранная или исключённая запись уже не кандидат этого словаря.
	ErrSelectionChanged = errs.New(
		errs.Conflict,
		"reference.selection_changed",
		"выбор содержит отсутствующую запись или не кандидата этого словаря",
	)
	// ErrNormalizer — словарь построен другой версией нормализации; нужна миграция пересчёта.
	ErrNormalizer = errs.New(
		errs.Conflict,
		"reference.normalizer_mismatch",
		"ключи словаря построены другой версией нормализации",
	)
	// ErrInvalidDelivery — поставка не прошла проверку до обращения к хранилищу.
	ErrInvalidDelivery = errs.New(errs.Invalid, "reference.invalid_delivery", "некорректная поставка реестра")
	// ErrDeliveryConflict — поставка понижает версию, подменяет данные или откат уже невозможен.
	ErrDeliveryConflict = errs.New(
		errs.Conflict,
		"reference.delivery_conflict",
		"поставка устарела, изменена или после неё были правки",
	)
	// ErrRevertConflict — запись изменилась после ревизии, которую откатывают.
	ErrRevertConflict = errs.New(
		errs.Conflict,
		"reference.revert_conflict",
		"запись изменилась после откатываемой ревизии",
	)
)
