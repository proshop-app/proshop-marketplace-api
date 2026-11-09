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
	tokens := fake.NewIdentityTokens(fake.NewClock(time.Now()))
	principal := operator.Principal{ID: "viewer", Role: "viewer", Email: "viewer@example.test"}
	cfg := appoperator.Config{Principal: principal, PasswordHash: "fake:secret", SessionTTL: time.Hour}
	access, err := tokens.Open(context.Background(), operator.Hash(cfg.Principal.ID+"\x00"+cfg.PasswordHash))
	if err != nil {
		t.Fatal(err)
	}

	auth := appoperator.NewAuth(
		tokens,
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
		changesLogin := route.path == "/api/v1/operator/session" &&
			(route.method == http.MethodPost || route.method == http.MethodDelete)
		if !strings.HasPrefix(route.path, "/api/v1/operator/") || changesLogin {
			continue
		}
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			path := strings.NewReplacer("{tenantId}", "t1", "{eventId}", "1", "{offerId}", "t1~p1").Replace(route.path)
			request := httptest.NewRequest(route.method, path, strings.NewReader(`{}`))
			request.Header.Set("X-Marketplace-Money-Unit", "RUB")
			request.AddCookie(&http.Cookie{Name: operatorCookie, Value: access.Token})
			request.AddCookie(&http.Cookie{Name: operatorKeyCookie, Value: access.Key})
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
