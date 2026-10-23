package http

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

func TestEveryCodeHasStatus(t *testing.T) {
	for _, code := range []errs.Code{errs.Invalid, errs.NotFound, errs.Conflict, errs.Unauthorized, errs.TooManyRequests, errs.Unavailable, errs.Internal} {
		if statusByCode[code] == 0 {
			t.Fatalf("у кода %s нет ответа в таблице", code)
		}
	}
}

func TestUnavailableMapsTo503(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)

	writeError(recorder, request, errs.New(errs.Unavailable, "storage.unavailable", "хранилище недоступно"))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("недоступная зависимость — 503: %d", recorder.Code)
	}
}

func TestObserveRecoversPanic(t *testing.T) {
	handler := observe(slog.New(slog.NewTextHandler(io.Discard, nil)), newHTTPMetrics(prometheus.NewRegistry()),
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("сбой") }))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusInternalServerError || recorder.Header().Get(headerRequestID) == "" {
		t.Fatalf("паника — 500 с идентификатором запроса: %d", recorder.Code)
	}
}
