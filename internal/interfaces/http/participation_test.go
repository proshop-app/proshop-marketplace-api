package http_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
)

func TestParticipationMachineAndReturn(t *testing.T) {
	store := fake.NewStore()
	clock := fake.NewClock(t0)
	store.SeedTenant(participant.TenantState{
		TenantID: "t1",
		Slug:     "shop",
		Status:   participant.TenantActive,
		Version:  1,
	})
	service := appparticipant.NewService(store, clock, &fake.IDs{})
	cmd := appparticipant.CreateCommand{TenantID: "t1", Name: "Магазин"}
	if _, err := service.Create(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	handler := transport.NewHandler(transport.Deps{
		Registerer:     prometheus.NewRegistry(),
		Logger:         slog.Default(),
		Participants:   service,
		OperatorToken:  token,
		ServerAPIToken: "server-token",
	})
	machine := "/api/v1/internal/tenants/t1/participation"
	operator := "/api/v1/operator/participants/t1"
	call := func(method, path, body, bearer string, status int) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Marketplace-Money-Unit", "RUB")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, recorder.Code, recorder.Body.String())
		}
		return recorder.Body.String()
	}
	for _, bearer := range []string{"", token, "wrong"} {
		call("GET", machine, "", bearer, http.StatusUnauthorized)
	}
	body := call("GET", machine, "", "server-token", http.StatusOK)
	if !strings.Contains(body, `"stage":"candidate"`) || !strings.Contains(body, `"awaitingReview":true`) {
		t.Fatal(body)
	}
	call("POST", machine+"/submit", "", "server-token", http.StatusConflict)
	for _, note := range []string{"", " ", strings.Repeat("я", 2001)} {
		call("POST", operator+"/return", `{"note":"`+note+`"}`, token, http.StatusBadRequest)
	}
	call("POST", operator+"/return", `{"reason":{"code":"missing"},"note":"Исправьте"}`, token, http.StatusBadRequest)
	body = call("POST", operator+"/return", `{"note":"Исправьте"}`, token, http.StatusOK)
	if !strings.Contains(body, `"stage":"returned"`) || !strings.Contains(body, `"lastDecision":{`) {
		t.Fatal(body)
	}
	call("POST", machine+"/submit", "", "server-token", http.StatusOK)
	call("POST", machine+"/submit", "", "server-token", http.StatusConflict)
	call("GET", "/api/v1/operator/participants?stage=unknown", "", token, http.StatusBadRequest)
	call("GET", "/api/v1/operator/participants?queue=unknown", "", token, http.StatusBadRequest)
	body = call("GET", "/api/v1/operator/participants?queue=true", "", token, http.StatusOK)
	if !strings.Contains(body, `"total":0`) {
		t.Fatal(body)
	}
	handler = transport.NewHandler(transport.Deps{
		Registerer:   prometheus.NewRegistry(),
		Logger:       slog.Default(),
		Participants: service,
	})
	call("GET", machine, "", "server-token", http.StatusNotFound)
}
