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
		DatabaseBudgets: ports.DatabaseBudgets{
			Read:       ports.DatabaseBudget{Statement: time.Second, Transaction: 5 * time.Second},
			Checkout:   ports.DatabaseBudget{Statement: 2 * time.Second, Transaction: 5 * time.Second},
			Ingestion:  ports.DatabaseBudget{Statement: 3 * time.Second, Transaction: 5 * time.Second},
			Background: ports.DatabaseBudget{Statement: 4 * time.Second, Transaction: 5 * time.Second},
		},
	}
	budgets := map[requestClass]ports.DatabaseBudget{
		readRequest:       deps.DatabaseBudgets.Read,
		checkoutRequest:   deps.DatabaseBudgets.Checkout,
		ingestionRequest:  deps.DatabaseBudgets.Ingestion,
		backgroundRequest: deps.DatabaseBudgets.Background,
		importRequest:     deps.DatabaseBudgets.Background,
	}
	for _, route := range routes(deps) {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			budget, ok := budgets[route.class]
			if !ok {
				t.Fatal("не задан класс маршрута")
			}
			for _, lateSuccess := range []bool{false, true} {
				next := func(w http.ResponseWriter, r *http.Request) {
					if got := ports.DatabaseBudgetFrom(r.Context(), ports.DatabaseBudget{}); got != budget {
						t.Fatal(got, budget)
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
