package catalog

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

var (
	// ErrInvalidSnapshot — снимок товара или тега не проходит проверку.
	ErrInvalidSnapshot = errs.New(errs.Invalid, "catalog.invalid_snapshot", "некорректный снимок каталога")
	// ErrInvalidSignal — сигнал остатка или цены не проходит проверку.
	ErrInvalidSignal = errs.New(errs.Invalid, "catalog.invalid_signal", "некорректный сигнал остатка или цены")
	// ErrProductNotFound — товара нет в проекции.
	ErrProductNotFound = errs.New(errs.NotFound, "catalog.product_not_found", "товара нет в проекции")
	// ErrSignalsNotFound — сигналов по товару ещё не было.
	ErrSignalsNotFound = errs.New(errs.NotFound, "catalog.signals_not_found", "сигналов по товару нет")
	// ErrConflict — запись проекции изменена параллельно.
	ErrConflict = errs.New(errs.Conflict, "catalog.version_conflict", "запись проекции изменена параллельно")
	// ErrOfferNotFound — предложения нет или оно скрыто.
	ErrOfferNotFound = errs.New(errs.NotFound, "catalog.offer_not_found", "предложение не найдено")
)
