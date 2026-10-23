package participant

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

var (
	// ErrNotFound — участника нет.
	ErrNotFound = errs.New(errs.NotFound, "participant.not_found", "участник не найден")
	// ErrAlreadyExists — магазин уже участвует.
	ErrAlreadyExists = errs.New(errs.Conflict, "participant.exists", "магазин уже участвует в маркетплейсе")
	// ErrSlugTaken — слаг занят другим участником.
	ErrSlugTaken = errs.New(errs.Conflict, "participant.slug_taken", "слаг занят другим участником")
	// ErrConflict — участника изменили параллельно; нужно перечитать.
	ErrConflict = errs.New(errs.Conflict, "participant.version_conflict", "участник изменён параллельно, перечитайте его")
	// ErrInvalidName — название пустое, слишком длинное или с управляющими символами.
	ErrInvalidName = errs.New(errs.Invalid, "participant.invalid_name", "название участника: от 1 до 200 символов без управляющих")
	// ErrInvalidPaymentAccount — идентификатор магазина продавца у платёжного провайдера некорректен.
	ErrInvalidPaymentAccount = errs.New(errs.Invalid, "participant.invalid_payment_account", "идентификатор магазина у провайдера: от 1 до 64 латинских букв, цифр, «_» и «-»")
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
