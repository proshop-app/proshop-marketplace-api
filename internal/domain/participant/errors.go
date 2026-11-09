package participant

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

var (
	// ErrInvalidCardProducts ограничивает закрепления тремя разными товарами.
	ErrInvalidCardProducts = errs.New(
		errs.Invalid,
		"participant.invalid_card_products",
		"некорректный список товаров карточки магазина",
	)
	// ErrInvalidCardAppearance запрещает неполный или некорректный цвет карточки магазина.
	ErrInvalidCardAppearance = errs.New(
		errs.Invalid,
		"participant.invalid_card_appearance",
		"некорректное оформление карточки магазина",
	)
	// ErrCardNotReady запрещает подключение до заполнения карточки.
	ErrCardNotReady = errs.New(errs.Conflict, "participant.card_not_ready", "карточка магазина не готова")
	// ErrInvalidCardKey ограничивает постмодерацию разрешёнными пунктами.
	ErrInvalidCardKey = errs.New(errs.Invalid, "participant.invalid_card_key", "неизвестный пункт карточки")
	// ErrInvalidStage — неизвестный фильтр стадии.
	ErrInvalidStage = errs.New(errs.Invalid, "participant.invalid_stage", "неизвестная стадия участия")
	// ErrInvalidNote требует объяснения операторского изменения.
	ErrInvalidNote = errs.New(errs.Invalid, "participant.invalid_note", "заметка: от 1 до 2000 символов")
	// ErrInvalidTrust запрещает неизвестный уровень доверия.
	ErrInvalidTrust = errs.New(errs.Invalid, "participant.invalid_trust", "неизвестный уровень доверия")
	// ErrNotFound — участника нет.
	ErrNotFound = errs.New(errs.NotFound, "participant.not_found", "участник не найден")
	// ErrAlreadyExists — магазин уже участвует.
	ErrAlreadyExists = errs.New(errs.Conflict, "participant.exists", "магазин уже участвует в маркетплейсе")
	// ErrSlugTaken — слаг занят другим участником.
	ErrSlugTaken = errs.New(errs.Conflict, "participant.slug_taken", "слаг занят другим участником")
	// ErrConflict — участника изменили параллельно; нужно перечитать.
	ErrConflict = errs.New(
		errs.Conflict,
		"participant.version_conflict",
		"участник изменён параллельно, перечитайте его",
	)
	// ErrInvalidName — название пустое, слишком длинное или с управляющими символами.
	ErrInvalidName = errs.New(
		errs.Invalid,
		"participant.invalid_name",
		"название участника: от 1 до 200 символов без управляющих",
	)

	// ErrTenantUnknown — магазина нет в реестре платформы.
	ErrTenantUnknown = errs.New(errs.Invalid, "participant.tenant_unknown", "магазина нет в реестре платформы")
	// ErrTenantUnavailable — магазин удаляется или удалён.
	ErrTenantUnavailable = errs.New(errs.Conflict, "participant.tenant_unavailable", "магазин удаляется или удалён")

	// ErrTenantNotFound — записи реестра нет.
	ErrTenantNotFound = errs.New(errs.NotFound, "registry.tenant_not_found", "магазина нет в реестре")
	// ErrTenantConflict — запись реестра изменена параллельно.
	ErrTenantConflict = errs.New(errs.Conflict, "registry.version_conflict", "запись реестра изменена параллельно")
	// ErrInvalidRegistryEvent — событие реестра не проходит проверку.
	ErrInvalidRegistryEvent = errs.New(errs.Invalid, "registry.invalid_event", "некорректное событие реестра")
)
