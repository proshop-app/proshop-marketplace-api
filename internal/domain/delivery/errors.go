package delivery

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

var (
	// ErrInvalidCarrier — код перевозчика не по формату.
	ErrInvalidCarrier = errs.New(errs.Invalid, "delivery.invalid_carrier", "некорректный перевозчик")
	// ErrUnknownCarrier — перевозчик не подключён.
	ErrUnknownCarrier = errs.New(errs.NotFound, "delivery.unknown_carrier", "перевозчик не подключён")
	// ErrInvalidPickupPoint — пункт выдачи от перевозчика не проходит проверку.
	ErrInvalidPickupPoint = errs.New(errs.Invalid, "delivery.invalid_pickup_point", "некорректный пункт выдачи")
	// ErrPickupPointNotFound — пункта нет в справочнике или он закрыт.
	ErrPickupPointNotFound = errs.New(errs.NotFound, "delivery.pickup_point_not_found", "пункт выдачи не найден")
	// ErrInvalidArea — область карты или город заданы неверно.
	ErrInvalidArea = errs.New(errs.Invalid, "delivery.invalid_area", "задайте область карты или город")
	// ErrInvalidQuote — ответ магазина о доставке не проходит проверку.
	ErrInvalidQuote = errs.New(errs.Invalid, "delivery.invalid_quote", "некорректный расчёт доставки")
)
