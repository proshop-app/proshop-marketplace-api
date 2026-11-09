package http

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

func TestEveryCodeHasStatus(t *testing.T) {
	codes := []errs.Code{
		errs.Invalid,
		errs.NotFound,
		errs.Conflict,
		errs.Unauthorized,
		errs.TooManyRequests,
		errs.Unavailable,
		errs.Internal,
	}

	for _, code := range codes {
		if statusByCode[code] == 0 {
			t.Fatalf("у кода %s нет ответа в таблице", code)
		}
	}
}

func TestUnavailableMapsTo503(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Marketplace-Money-Unit", "RUB")

	writeError(recorder, request, errs.New(errs.Unavailable, "storage.unavailable", "хранилище недоступно"))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("недоступная зависимость — 503: %d", recorder.Code)
	}
}

func TestObserveRecoversPanic(t *testing.T) {
	handler := observe(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		newHTTPMetrics(prometheus.NewRegistry()),
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("сбой") }),
	)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusInternalServerError || recorder.Header().Get(headerRequestID) == "" {
		t.Fatalf("паника — 500 с идентификатором запроса: %d", recorder.Code)
	}
}

func TestObserveLogsBuyer(t *testing.T) {
	var logs bytes.Buffer

	handler := observe(
		slog.New(slog.NewJSONHandler(&logs, nil)),
		newHTTPMetrics(prometheus.NewRegistry()),
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			rememberBuyer(r.Context(), "8a1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b")
		}),
	)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))

	if !strings.Contains(logs.String(), `"buyerId":"8a1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b"`) {
		t.Fatalf("строка журнала запроса с покупателем: %s", logs.String())
	}
}
