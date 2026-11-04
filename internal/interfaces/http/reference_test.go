package http_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const readToken = "reference-reader-token-0123456789abcdef"

type referenceRoute struct {
	method, path, body, resource string
	write                        bool
}

func referenceHandler(api ports.ReferenceAPI) http.Handler {
	return transport.NewHandler(transport.Deps{
		Reference:          api,
		OperatorToken:      token,
		ReferenceReadToken: readToken,
		Registerer:         prometheus.NewRegistry(),
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}
func referenceRequest(handler http.Handler, method, path, bearer, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"12"`)
	request.Header.Set("X-Reference-Actor", "forged-browser-actor")
	reply := httptest.NewRecorder()
	handler.ServeHTTP(reply, request)
	return reply
}
func TestReferenceRoutesKeepGatesAndDelegateToOnlyOwner(t *testing.T) {
	routes := []referenceRoute{
		{method: http.MethodGet, path: "snapshot?since=12", resource: "snapshot"},
		{method: http.MethodPost, path: "resolve", body: `{"text":"Значение"}`, resource: "resolve"},
		{method: http.MethodGet, path: "entries?status=confirmed", resource: "entries"},
		{method: http.MethodGet, path: "entries/e1", resource: "entries/e1"},
		{method: http.MethodPost, path: "entries", body: `{"canonical":"Значение"}`, resource: "entries", write: true},
		{
			method:   http.MethodPatch,
			path:     "entries/e1",
			body:     `{"canonical":"Другое"}`,
			resource: "entries/e1",
			write:    true,
		},
		{
			method:   http.MethodPost,
			path:     "entries/e1/merge",
			body:     `{"into":"e2"}`,
			resource: "entries/e1/merge",
			write:    true,
		},
		{
			method:   http.MethodPost,
			path:     "entries/e1/aliases",
			body:     `{"text":"Variant"}`,
			resource: "entries/e1/aliases",
			write:    true,
		},
		{method: http.MethodDelete, path: "aliases/a1", resource: "aliases/a1", write: true},
	}
	for _, route := range routes {
		t.Run(route.method+route.path, func(t *testing.T) {
			response := ports.ReferenceResponse{
				Status: 200,
				Body:   json.RawMessage(`{"schemaVersion":1,"revision":13}`),
				ETag:   `"13"`,
			}
			api := &fake.ReferenceAPI{Response: response}
			handler := referenceHandler(api)
			path := "/internal/reference/b1/" + route.path
			if got := referenceRequest(handler, route.method, path, "", route.body); got.Code != 401 {
				t.Fatalf("без токена: %d", got.Code)
			}
			if route.write {
				if got := referenceRequest(handler, route.method, path, readToken, route.body); got.Code != 401 {
					t.Fatalf("читатель пишет: %d", got.Code)
				}
			}
			if len(api.Calls()) != 0 {
				t.Fatal("неавторизованный запрос дошёл до владельца")
			}
			bearer := readToken
			if route.write {
				bearer = token
			}
			got := referenceRequest(handler, route.method, path, bearer, route.body)
			if got.Code != 200 || got.Header().Get("ETag") != `"13"` ||
				got.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("ответ: %d %s", got.Code, got.Body)
			}
			calls := api.Calls()
			if len(calls) != 1 {
				t.Fatalf("запросов: %d", len(calls))
			}
			call := calls[0]
			if call.Dictionary != "b1" || call.Resource != route.resource || call.Write != route.write ||
				string(call.Body) != route.body || call.IfMatch != `"12"` {
				t.Fatalf("делегирование: %+v", call)
			}
			if route.write && call.Actor != "machine" || !route.write && call.Actor != "" {
				t.Fatalf("недоверенный actor: %q", call.Actor)
			}
		})
	}
}
func TestReferenceProxyBoundsAndFailure(t *testing.T) {
	response := ports.ReferenceResponse{
		Status: 409,
		Body:   json.RawMessage(`{"error":{"code":"REFERENCE_ENTRY_CHANGED","message":"изменено"}}`),
	}
	api := &fake.ReferenceAPI{Response: response}
	handler := referenceHandler(api)
	reply := referenceRequest(handler, http.MethodPatch, "/internal/reference/b1/entries/e1", token, `{}`)
	if reply.Code != 409 || !strings.Contains(reply.Body.String(), "REFERENCE_ENTRY_CHANGED") {
		t.Fatalf("upstream конфликт: %d %s", reply.Code, reply.Body)
	}
	for _, body := range []string{`{`, strings.Repeat("x", (4<<10)+1)} {
		reply := referenceRequest(handler, http.MethodPost, "/internal/reference/b1/resolve", readToken, body)
		if reply.Code != 400 {
			t.Fatalf("негодное тело: %d", reply.Code)
		}
	}
	api.Err = errs.New(errs.Unavailable, "reference.unavailable", "недоступно")
	reply = referenceRequest(handler, http.MethodGet, "/internal/reference/b1/snapshot", readToken, "")
	if reply.Code != 503 {
		t.Fatalf("сбой владельца: %d", reply.Code)
	}
}
