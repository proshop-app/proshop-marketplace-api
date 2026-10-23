// Package http — REST маркетплейса: каталог для витрины, операторские ручки
// участников и служебные адреса. Обработчики тонкие: разбор запроса, вызов
// сценария приложения, ответ.
package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
)

const checkTimeout = 2 * time.Second

// Check — проверка зависимости для /health.
type Check struct {
	Name  string
	Probe func(ctx context.Context) error
}

// Deps — сценарии и служебное, из которых собираются маршруты.
type Deps struct {
	Catalog       *appcatalog.Queries
	Participants  *appparticipant.Service
	OperatorToken string
	Checks        []Check
	Metrics       http.Handler
	Registerer    prometheus.Registerer
	Logger        *slog.Logger
}

// NewHandler — маршруты маркетплейса.
func NewHandler(deps Deps) http.Handler {
	catalog := &catalogHandlers{queries: deps.Catalog}
	participants := &participantHandlers{service: deps.Participants}
	operator := func(next http.HandlerFunc) http.HandlerFunc { return operatorOnly(deps.OperatorToken, next) }

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health(deps.Checks))
	mux.Handle("GET /metrics", deps.Metrics)

	mux.HandleFunc("GET /api/v1/catalog/offers", catalog.list)
	mux.HandleFunc("GET /api/v1/catalog/offers/{offerId}", catalog.card)

	mux.HandleFunc("GET /api/v1/operator/participants", operator(participants.list))
	mux.HandleFunc("POST /api/v1/operator/participants", operator(participants.create))
	mux.HandleFunc("GET /api/v1/operator/participants/{tenantId}", operator(participants.get))
	mux.HandleFunc("PATCH /api/v1/operator/participants/{tenantId}", operator(participants.rename))
	mux.HandleFunc("PUT /api/v1/operator/participants/{tenantId}/payment-account", operator(participants.setPaymentAccount))
	mux.HandleFunc("POST /api/v1/operator/participants/{tenantId}/enable", operator(participants.enable))
	mux.HandleFunc("POST /api/v1/operator/participants/{tenantId}/disable", operator(participants.disable))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { writeError(w, r, ErrRouteNotFound) })

	return observe(deps.Logger, newHTTPMetrics(deps.Registerer), mux)
}

type healthJSON struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// health — 200, если все зависимости отвечают, иначе 503; подробности сбоя — в журнале.
func health(checks []Check) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := healthJSON{Status: "ok", Checks: make(map[string]string, len(checks))}
		status := http.StatusOK

		for _, check := range checks {
			ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
			err := check.Probe(ctx)
			cancel()

			if err != nil {
				body.Status, body.Checks[check.Name], status = "unavailable", "fail", http.StatusServiceUnavailable
				loggerFrom(r.Context()).Warn("health: зависимость не отвечает", slog.String("check", check.Name), slog.Any("error", err))

				continue
			}

			body.Checks[check.Name] = "ok"
		}

		writeJSON(w, status, body)
	}
}
