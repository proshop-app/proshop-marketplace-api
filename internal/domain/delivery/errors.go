package delivery

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

var (
	// ErrRegionNotAllowed отличает область доставки магазина от пустого справочника перевозчика.
	ErrRegionNotAllowed = errs.New(
		errs.Invalid,
		"delivery.region_not_allowed",
		"магазин не доставляет в регион выбранного города",
	)
	// ErrInvalidCarrier — код перевозчика не по формату.
	ErrInvalidCarrier = errs.New(errs.Invalid, "delivery.invalid_carrier", "некорректный перевозчик")
	// ErrUnknownCarrier — перевозчик не подключён.
	ErrUnknownCarrier = errs.New(errs.NotFound, "delivery.unknown_carrier", "перевозчик не подключён")
	// ErrInvalidPickupPoint — пункт выдачи от перевозчика не проходит проверку.
	ErrInvalidPickupPoint = errs.New(errs.Invalid, "delivery.invalid_pickup_point", "некорректный пункт выдачи")
	// ErrPickupPointNotFound — пункта нет в списке магазина.
	ErrPickupPointNotFound = errs.New(errs.NotFound, "delivery.pickup_point_not_found", "пункт выдачи не найден")
	// ErrInvalidCity — город обязателен и ограничен контрактом.
	ErrInvalidCity = errs.New(errs.Invalid, "delivery.invalid_city", "city — от 1 до 120 символов")
	// ErrInvalidQuote — ответ магазина о доставке не проходит проверку.
	ErrInvalidQuote = errs.New(errs.Invalid, "delivery.invalid_quote", "некорректный расчёт доставки")
	// ErrSelectionBusy — покупатель уже проверяет ПВЗ в другом холодном справочнике.
	ErrSelectionBusy = errs.New(errs.TooManyRequests, "delivery.selection_busy", "дождитесь проверки выбранного ПВЗ")
)
