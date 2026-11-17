package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestRequestBudgetCancelsWorkAndSelectsDatabaseClass(t *testing.T) {
	read := ports.DatabaseBudget{Statement: time.Millisecond, Transaction: time.Second}
	checkout := ports.DatabaseBudget{Statement: 2 * time.Millisecond, Transaction: time.Second}
	deps := Deps{
		RequestTimeout:  20 * time.Millisecond,
		DatabaseBudgets: ports.DatabaseBudgets{Read: read, Checkout: checkout},
	}
	for _, path := range []string{"/api/v1/catalog/offers", "/api/v1/checkout"} {
		t.Run(path, func(t *testing.T) {
			next := func(_ http.ResponseWriter, r *http.Request) {
				want := read
				if path == "/api/v1/checkout" {
					want = checkout
				}
				if got := ports.DatabaseBudgetFrom(r.Context(), ports.DatabaseBudget{}); got != want {
					t.Fatal(got, want)
				}
				select {
				case <-r.Context().Done():
					if !errors.Is(r.Context().Err(), context.DeadlineExceeded) {
						t.Error(r.Context().Err())
					}
				case <-time.After(time.Second):
					t.Error("срок HTTP не дошёл до обработчика")
				}
			}
			requestBudget(path, deps, next)(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		})
	}
}
