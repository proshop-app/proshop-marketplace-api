package http_test

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
)

func TestStatusMachineScopeAndLimits(t *testing.T) {
	store := fake.NewStore()
	store.SeedParticipant(participant.State{
		TenantID: "t1",
		Stage:    participant.StageConnected,
		Enabled:  true,
		Trust:    participant.Probation,
	})
	store.SeedProduct(catalog.ProductState{
		TenantID:  "t1",
		ProductID: "p1",
		Revision:  7,
	})
	service := moderationstatus.NewService(store, fake.NewClock(t0), &fake.Publisher{}, "offer", "participant")
	handler := transport.NewHandler(transport.Deps{
		Statuses:       service,
		ServerAPIToken: "server-token",
		Registerer:     prometheus.NewRegistry(),
		Logger:         slog.Default(),
	})
	for _, tc := range []struct {
		path   string
		token  string
		status int
		items  int
	}{
		{"t1/status?ids=p1", "", 401, 0},
		{"t1/status?ids=p1", token, 401, 0},
		{"t1/status?ids=p1", "wrong", 401, 0},
		{"t1/status?ids=p1,missing", "server-token", 200, 1},
		{"t1/status?ids=p1,p1", "server-token", 200, 1},
		{"t1/status?ids=" + strings.Repeat("p1,", 199) + "p1", "server-token", 200, 1},
		{"t1/status?ids=" + strings.Repeat("p1,", 200) + "p1", "server-token", 400, 0},
		{"t1/status?ids=", "server-token", 400, 0},
		{"t1/status?ids=p1,,p2", "server-token", 400, 0},
		{"t1/status?ids=p1&ids=p2", "server-token", 400, 0},
		{"bad%20tenant/status", "server-token", 400, 0},
		{"t1/status", "server-token", 200, 0},
		{"t2/status?ids=p1", "server-token", 200, 0},
		{"t1/status/products/p1", "server-token", 404, 0},
	} {
		t.Run(tc.path+tc.token, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/internal/tenants/"+tc.path, nil)
			r.Header.Set("Authorization", "Bearer "+tc.token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			if w.Code != http.StatusOK {
				return
			}
			var snapshot moderationstatus.Snapshot
			if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Items == nil || len(snapshot.Items) != tc.items {
				t.Fatal(w.Body.String())
			}
			if tc.items > 0 && (snapshot.Items[0].Status != "hidden" || snapshot.Items[0].Revision != 7) {
				t.Fatal("товар без допуска не скрыт", w.Body.String())
			}
			if strings.HasPrefix(tc.path, "t2/") {
				if snapshot.Participant != nil {
					t.Fatal("ответ содержит чужого участника")
				}
			} else if snapshot.Participant == nil || snapshot.Participant.TenantID != "t1" {
				t.Fatal(w.Body.String())
			}
		})
	}
	store.FailCommit = errors.New("database unavailable")
	r := httptest.NewRequest(http.MethodGet, "/api/v1/internal/tenants/t1/status?ids=p1", nil)
	r.Header.Set("Authorization", "Bearer server-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), `"items"`) {
		t.Fatal("сбой чтения вернул частичные статусы", w.Code, w.Body.String())
	}
	for _, machine := range []string{"", "server-token"} {
		handler = transport.NewHandler(transport.Deps{
			ServerAPIToken: machine,
			Registerer:     prometheus.NewRegistry(),
			Logger:         slog.Default(),
		})
		r := httptest.NewRequest(http.MethodGet, "/api/v1/internal/tenants/t1/status", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if machine == "" && w.Code != http.StatusNotFound || machine != "" && w.Code != http.StatusUnauthorized {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
