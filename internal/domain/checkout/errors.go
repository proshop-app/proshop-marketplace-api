package checkout

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

var (
	// ErrNotFound — покупки нет.
	ErrNotFound = errs.New(errs.NotFound, "checkout.not_found", "покупка не найдена")
	// ErrEmpty — покупать нечего.
	ErrEmpty = errs.New(errs.Invalid, "checkout.empty", "корзина пуста")
	// ErrInvalidIdempotencyKey — ключ идемпотентности не по формату.
	ErrInvalidIdempotencyKey = errs.New(errs.Invalid, "checkout.invalid_idempotency_key", "заголовок Idempotency-Key: от 8 до 128 латинских букв, цифр и «-_:.»")
	// ErrDuplicateKey — покупка с этим ключом уже заведена.
	ErrDuplicateKey = errs.New(errs.Conflict, "checkout.duplicate_key", "покупка с этим ключом уже есть")
	// ErrNotDraft — покупка уже подтверждена.
	ErrNotDraft = errs.New(errs.Conflict, "checkout.not_draft", "покупка уже подтверждена")
	// ErrDeliveryNotSelected — пункт выдачи не выбран.
	ErrDeliveryNotSelected = errs.New(errs.Conflict, "checkout.delivery_not_selected", "выберите перевозчика и пункт выдачи")
	// ErrDeliveryUnavailable — кто-то из продавцов не везёт в выбранный пункт.
	ErrDeliveryUnavailable = errs.New(errs.Conflict, "checkout.delivery_unavailable", "не все продавцы доставляют в выбранный пункт")
	// ErrQuoteExpired — цена доставки устарела.
	ErrQuoteExpired = errs.New(errs.Conflict, "checkout.quote_expired", "расчёт доставки устарел, пересчитайте")
	// ErrChanged — магазины поменяли цены; покупка обновлена и ждёт подтверждения заново.
	ErrChanged = errs.New(errs.Conflict, "checkout.changed", "цены изменились, проверьте покупку")
	// ErrOfferUnavailable — магазин больше не продаёт предложение или его не хватает.
	ErrOfferUnavailable = errs.New(errs.Conflict, "checkout.offer_unavailable", "часть товаров недоступна, вернитесь в корзину")
	// ErrInvalidQuotes — расчётов не столько, сколько продавцов.
	ErrInvalidQuotes = errs.New(errs.Internal, "checkout.invalid_quotes", "расчёты доставки не совпадают с продавцами покупки")
	// ErrConflict — покупку изменили параллельно.
	ErrConflict = errs.New(errs.Conflict, "checkout.version_conflict", "покупка изменена параллельно, повторите")
)
