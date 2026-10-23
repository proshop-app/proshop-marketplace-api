package cart

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

var (
	// ErrNotFound — корзины нет.
	ErrNotFound = errs.New(errs.NotFound, "cart.not_found", "корзина не найдена")
	// ErrLineNotFound — такого предложения в корзине нет.
	ErrLineNotFound = errs.New(errs.NotFound, "cart.line_not_found", "предложения нет в корзине")
	// ErrInvalidQuantity — количество вне допустимого.
	ErrInvalidQuantity = errs.New(errs.Invalid, "cart.invalid_quantity", "количество — от 1 до 999")
	// ErrTooManyLines — в корзине предел позиций.
	ErrTooManyLines = errs.New(errs.Invalid, "cart.too_many_lines", "в корзине не больше 100 позиций")
	// ErrOfferUnavailable — предложение скрыто или его нет.
	ErrOfferUnavailable = errs.New(errs.Conflict, "cart.offer_unavailable", "предложение недоступно")
	// ErrNotEnoughStock — в наличии меньше, чем просят.
	ErrNotEnoughStock = errs.New(errs.Conflict, "cart.not_enough_stock", "в наличии меньше, чем в корзине")
	// ErrConflict — корзину изменили параллельно.
	ErrConflict = errs.New(errs.Conflict, "cart.version_conflict", "корзина изменена параллельно, повторите")
)
