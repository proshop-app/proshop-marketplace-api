package http_test

import (
	"net/http"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
)

func TestOutboxFailuresRequiresOperatorAndValidCursor(t *testing.T) {
	e := newEnv(t, func(deps *transport.Deps) {
		deps.Dispatcher = messaging.NewDispatcher(fake.NewStore(), fake.NewClock(t0), messaging.DispatcherConfig{}, nil)
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
