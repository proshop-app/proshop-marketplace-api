package http

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

var (
	// ErrForbidden — недоверенный браузерный origin.
	ErrForbidden = errs.New(errs.Forbidden, "http.origin_forbidden", "недопустимый источник запроса")
	// ErrInvalidBody — тело запроса не разбирается.
	ErrInvalidBody = errs.New(errs.Invalid, "http.invalid_body", "тело запроса не разбирается")
	// ErrInvalidQuery — параметры адреса не разбираются.
	ErrInvalidQuery = errs.New(errs.Invalid, "http.invalid_query", "некорректные параметры запроса")
	// ErrUnauthorized — нет служебного токена оператора или он неверен.
	ErrUnauthorized = errs.New(errs.Unauthorized, "http.unauthorized", "нужен служебный токен оператора")
	// ErrRouteNotFound — нет такого адреса.
	ErrRouteNotFound = errs.New(errs.NotFound, "http.not_found", "адрес не найден")
)

// statusByCode — единственная таблица перевода ошибок в коды ответа. Доменная
// ошибка несёт класс, транспорт выбирает ответ только по нему.
var statusByCode = map[errs.Code]int{
	errs.Forbidden:       http.StatusForbidden,
	errs.Invalid:         http.StatusBadRequest,
	errs.NotFound:        http.StatusNotFound,
	errs.Conflict:        http.StatusConflict,
	errs.Unauthorized:    http.StatusUnauthorized,
	errs.TooManyRequests: http.StatusTooManyRequests,
	errs.Unavailable:     http.StatusServiceUnavailable,
	errs.Internal:        http.StatusInternalServerError,
}

type errorBody struct {
	Case     *moderationCaseJSON `json:"case,omitempty"`
	Error    errorDetail         `json:"error"`
	Checkout *checkoutJSON       `json:"checkout,omitempty"`
	Event    *parkedEventJSON    `json:"event,omitempty"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError отвечает ошибкой по таблице; внутренние ошибки не раскрываются клиенту.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	writeErrorWithCheckout(w, r, err, nil)
}

func writeErrorWithCheckout(w http.ResponseWriter, r *http.Request, err error, state *checkoutJSON) {
	writeErrorWithState(w, r, err, state, nil)
}

func writeErrorWithEvent(w http.ResponseWriter, r *http.Request, err error, state *parkedEventJSON) {
	writeErrorWithState(w, r, err, nil, state)
}

func writeErrorWithState(
	w http.ResponseWriter,
	r *http.Request,
	err error,
	checkout *checkoutJSON,
	event *parkedEventJSON,
) {
	code := errs.CodeOf(err)
	status := statusByCode[code]

	detail := errorDetail{Code: string(errs.Internal), Message: "внутренняя ошибка"}
	if typed, ok := errs.As(err); ok && code != errs.Internal {
		detail = errorDetail{Code: typed.Key(), Message: typed.Message()}
	}

	if status >= http.StatusInternalServerError {
		loggerFrom(r.Context()).Error("http: запрос не выполнен", slog.Int("status", status), slog.Any("error", err))
	}

	switch detail.Code {
	case "event.idempotency_key_reused", "moderation.idempotency_key_reused":
		detail.Code = "IDEMPOTENCY_KEY_REUSED"
	case "checkout.delivery_changed":
		detail.Code = "DELIVERY_CHANGED"
	case "checkout.payment_order":
		detail.Code = "PAYMENT_ORDER"
	case "reference.entry_changed":
		detail.Code = "REFERENCE_ENTRY_CHANGED"
	case "reference.key_taken":
		detail.Code = "REFERENCE_KEY_TAKEN"
	}
	body := errorBody{Error: detail, Checkout: checkout, Event: event}
	var conflict *moderation.CaseConflict
	if errors.As(err, &conflict) {
		current := caseJSON(conflict.Current)
		body.Case = &current
	}
	writeJSON(w, status, body)
}
