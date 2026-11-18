package ports

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

// PlatformServerFailure отличает отказ соединения или шлюза от сбоя конкретного магазина.
// Адаптер сохраняет исходную ошибку и её договор через Unwrap.
type PlatformServerFailure struct {
	Cause error
}

// PickupCatalogFailure относится к общему справочнику перевозчика, не к готовности магазина.
type PickupCatalogFailure struct {
	Cause error
}

// ErrPickupCatalogUnavailable требует повтора загрузки, не исправления покупки.
var ErrPickupCatalogUnavailable = errs.New(
	errs.Unavailable,
	"delivery.catalog_unavailable",
	"справочник пунктов временно недоступен; повторите поиск",
)

func (e *PlatformServerFailure) Error() string {
	return e.Cause.Error()
}

// Unwrap сохраняет код ошибки и проверку errors.Is.
func (e *PlatformServerFailure) Unwrap() error {
	return e.Cause
}

func (e *PickupCatalogFailure) Error() string { return e.Cause.Error() }

// Unwrap задаёт временный класс сбоя, сохраняя исходный ответ для перезапуска страниц.
func (e *PickupCatalogFailure) Unwrap() error { return ErrPickupCatalogUnavailable.Wrap(e.Cause) }
