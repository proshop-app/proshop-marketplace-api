package promotion

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

// Значения контракта продвижения не зависят от конкретного магазина.
var (
	ErrRateLimited          = errs.New(errs.TooManyRequests, "promotion.rate_limited", "лимит маячков исчерпан")
	ErrUnauthorized         = errs.New(errs.Unauthorized, "promotion.unauthorized", "нужен служебный токен")
	ErrInvalid              = errs.New(errs.Invalid, "promotion.invalid", "некорректное размещение")
	ErrNotFound             = errs.New(errs.NotFound, "promotion.not_found", "размещение не найдено")
	ErrConflict             = errs.New(errs.Conflict, "promotion.conflict", "размещение изменено")
	ErrKindUnsupported      = errs.New(errs.Invalid, "promotion.kind_unsupported", "вид продвижения не поддерживается")
	ErrActionUnsupported    = errs.New(errs.Invalid, "promotion.action_unsupported", "действие не поддерживается")
	ErrMarkingRequired      = errs.New(errs.Invalid, "promotion.marking_required", "нужны реквизиты рекламодателя")
	ErrERIDRequired         = errs.New(errs.Invalid, "promotion.erid_required", "нужен erid")
	ErrOfferWithoutCategory = errs.New(errs.Invalid, "promotion.offer_without_category", "нет категории площадки")
	ErrNoCapacity           = errs.New(errs.Conflict, "promotion.no_capacity", "нет свободных слотов")
)

// CapacityError возвращает занятые недели вместе со стабильным ключом конфликта.
type CapacityError struct{ Weeks []string }

// Error сохраняет контракт продвижения между слоями.
func (e *CapacityError) Error() string { return ErrNoCapacity.Error() }

// Unwrap сохраняет контракт продвижения между слоями.
func (e *CapacityError) Unwrap() error { return ErrNoCapacity }
