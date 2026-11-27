package http_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
)

func TestOutboxFailuresRequiresOperatorAndValidCursor(t *testing.T) {
	e := newEnv(t, func(deps *transport.Deps) {
		deps.Dispatcher = messaging.NewDispatcher(
			fake.NewStore(),
			fake.NewClock(t0),
			messaging.DispatcherConfig{},
			nil,
		)
	})
	path := "/api/v1/operator/outbox/failures"
	if result := e.call(t, http.MethodGet, path, "", false); result.status != http.StatusUnauthorized {
		t.Fatal(result)
	}
	if result := e.call(t, http.MethodGet, path, "", true); result.status != http.StatusOK {
		t.Fatal(result)
	}
	for _, query := range []string{"?after=-1", "?limit=0", "?limit=101", "?after=invalid"} {
		if result := e.call(t, http.MethodGet, path+query, "", true); result.status != http.StatusBadRequest {
			t.Fatal(query, result)
		}
	}
}

func TestOperatorCanReturnQuarantineByKindAndID(t *testing.T) {
	store := fake.NewStore()
	clock := fake.NewClock(t0)
	dispatcher := messaging.NewDispatcher(
		store,
		clock,
		messaging.DispatcherConfig{
			Batch: 10,
			Lease: time.Second,
		}, nil)
	for _, kind := range []string{"bad", "other", "bad"} {
		err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
			return tx.Outbox().Enqueue(ctx, ports.NewTask{
				Kind:     kind,
				TenantID: "t1",
				Payload:  []byte("{}"),
				At:       t0,
			})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		if _, _, _, err := dispatcher.RunWork(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	e := newEnv(t, func(deps *transport.Deps) { deps.Dispatcher = dispatcher })
	path := "/api/v1/operator/outbox/failures/retry"
	unauthorized := e.call(t, http.MethodPost, path, `{"kind":"bad"}`, false)
	if unauthorized.status != http.StatusUnauthorized {
		t.Fatal(unauthorized)
	}
	for _, body := range []string{`{}`, `{"kind":"bad","limit":101}`, `{"kind":"bad","after":"-1"}`} {
		if response := e.call(t, http.MethodPost, path, body, true); response.status != http.StatusBadRequest {
			t.Fatal(response)
		}
	}
	for _, after := range []string{"0", "1", "3"} {
		response := e.call(t, http.MethodPost, path, `{"kind":"bad","limit":1,"after":"`+after+`"}`, true)
		if response.status != http.StatusOK {
			t.Fatal(response)
		}
	}
	path = "/api/v1/operator/outbox/failures/2/retry"
	if response := e.call(t, http.MethodPost, path, "", true); response.status != http.StatusOK {
		t.Fatal(response)
	}
	if response := e.call(t, http.MethodPost, path, "", true); response.status != http.StatusNotFound {
		t.Fatal(response)
	}
	if rows, err := dispatcher.Failures(context.Background(), 0, 100); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}
