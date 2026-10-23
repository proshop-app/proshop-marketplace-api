package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appoperator "github.com/proshop-app/proshop-marketplace/internal/application/operator"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
)

func TestEveryOperatorRouteChecksServerRole(t *testing.T) {
	sessions := fake.NewOperatorSessions()
	principal := operator.Principal{ID: "viewer", Role: "viewer", Email: "viewer@example.test"}
	cfg := appoperator.Config{Principal: principal, PasswordHash: "fake:secret", SessionTTL: time.Hour}
	if err := sessions.Save(context.Background(), operator.Session{
		Principal:      principal,
		TokenHash:      operator.Hash("viewer-token"),
		CredentialHash: operator.Hash(cfg.PasswordHash),
		ExpiresAt:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	auth := appoperator.NewAuth(
		sessions,
		fake.NewLimiter(),
		fake.Passwords{},
		&fake.IDs{},
		fake.NewClock(time.Now()),
		cfg,
	)
	deps := Deps{
		OperatorAuth:   auth,
		OperatorOrigin: "https://admin.example",
		Registerer:     prometheus.NewRegistry(),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	handler := NewHandler(deps)
	for _, route := range routes(deps) {
		if !strings.HasPrefix(route.path, "/api/v1/operator/") ||
			(route.path == "/api/v1/operator/session" && route.method == http.MethodPost) {
			continue
		}
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			path := strings.NewReplacer("{tenantId}", "t1", "{eventId}", "1", "{offerId}", "t1~p1").Replace(route.path)
			request := httptest.NewRequest(route.method, path, strings.NewReader(`{}`))
			request.AddCookie(&http.Cookie{Name: operatorCookie, Value: "viewer-token"})
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", deps.OperatorOrigin)
			request.Header.Set("X-Requested-With", "KvazarOperator")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatal(response.Code, response.Body.String())
			}
		})
	}
}
