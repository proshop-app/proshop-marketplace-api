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
		RequestTimeout:         -time.Second,
		ReferenceImportTimeout: -time.Second,
	}
	classes := map[requestClass]ports.DatabaseClass{
		readRequest:       ports.DatabaseRead,
		checkoutRequest:   ports.DatabaseCheckout,
		writeRequest:      ports.DatabaseWrite,
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
	ctx, cancel := context.WithDeadline(context.Background(), time.Time{})
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/any", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	writeError(recorder, request, context.DeadlineExceeded)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatal("истечение срока стало внутренней ошибкой", recorder.Code, recorder.Body.String())
	}
}

func TestAuxiliaryWritesUseTheirOwnDatabaseClass(t *testing.T) {
	want := map[string]requestClass{
		"POST /api/v1/checkout":         checkoutRequest,
		"POST /api/v1/cart/lines":       checkoutRequest,
		"POST /api/v1/promotion/events": writeRequest,
		"PATCH /api/v1/buyer/profile":   writeRequest,
		"POST /api/v1/auth/call":        writeRequest,
	}
	for _, route := range routes(Deps{}) {
		key := route.method + " " + route.path
		if class, ok := want[key]; ok {
			if route.class != class {
				t.Errorf("%s: класс %d, ожидается %d", key, route.class, class)
			}
			delete(want, key)
		}
	}
	if len(want) != 0 {
		t.Fatal("не найдены маршруты", want)
	}
}
