package http

import (
	"log/slog"
	"net/http"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

var (
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
	errs.Invalid:      http.StatusBadRequest,
	errs.NotFound:     http.StatusNotFound,
	errs.Conflict:     http.StatusConflict,
	errs.Unauthorized: http.StatusUnauthorized,
	errs.Unavailable:  http.StatusServiceUnavailable,
	errs.Internal:     http.StatusInternalServerError,
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError отвечает ошибкой по таблице; внутренние ошибки не раскрываются клиенту.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	code := errs.CodeOf(err)
	status := statusByCode[code]

	detail := errorDetail{Code: string(errs.Internal), Message: "внутренняя ошибка"}
	if typed, ok := errs.As(err); ok && code != errs.Internal {
		detail = errorDetail{Code: typed.Key(), Message: typed.Message()}
	}

	if status >= http.StatusInternalServerError {
		loggerFrom(r.Context()).Error("http: запрос не выполнен", slog.Int("status", status), slog.Any("error", err))
	}

	writeJSON(w, status, errorBody{Error: detail})
}
