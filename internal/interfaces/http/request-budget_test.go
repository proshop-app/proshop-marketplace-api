package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestEveryRouteHasExplicitBudgetAndUniformTimeoutStatus(t *testing.T) {
	deps := Deps{
		RequestTimeout:         time.Millisecond,
		ReferenceImportTimeout: 2 * time.Millisecond,
	}
	classes := map[requestClass]ports.DatabaseClass{
		readRequest:       ports.DatabaseRead,
		checkoutRequest:   ports.DatabaseCheckout,
		writeRequest:      ports.DatabaseCheckout,
		backgroundRequest: ports.DatabaseBackground,
		importRequest:     ports.DatabaseBackground,
	}
	for _, route := range routes(deps) {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			class, ok := classes[route.class]
			if !ok {
				t.Fatal("не задан класс маршрута")
			}
			for _, lateSuccess := range []bool{false, true} {
				next := func(w http.ResponseWriter, r *http.Request) {
					if got := ports.DatabaseClassFrom(r.Context()); got != class {
						t.Fatal(got, class)
					}
					<-r.Context().Done()
					if lateSuccess {
						writeJSON(w, http.StatusOK, true)
						return
					}
					writeError(w, r, context.DeadlineExceeded)
				}
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(route.method, "/any-path/operator/checkout", nil)
				requestBudget(route.class, deps, next)(recorder, request)
				if recorder.Code != http.StatusServiceUnavailable {
					t.Fatal("срок должен отвечать 503 для любого маршрута", recorder.Code, recorder.Body.String())
				}
			}
		})
	}
}

func TestDeadlineErrorUses503BeforeWritingHeaders(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/any", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	writeError(recorder, request, context.DeadlineExceeded)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatal("истечение срока стало внутренней ошибкой", recorder.Code, recorder.Body.String())
	}
}
