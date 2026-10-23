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

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
)

const checkTimeout = 2 * time.Second

// Check — проверка зависимости для /health.
type Check struct {
	Name  string
	Probe func(ctx context.Context) error
}

// Deps — сценарии и служебное, из которых собираются маршруты.
type Deps struct {
	Taxonomy      *apptaxonomy.Service
	Catalog       *appcatalog.Queries
	Participants  *appparticipant.Service
	Auth          *appbuyer.Auth
	Carts         *appcart.Service
	Directory     *appdelivery.Directory
	Checkouts     *appcheckout.Service
	OperatorToken string
	// TrustProxy — адрес покупателя брать из X-Forwarded-For: сервис за доверенным прокси.
	TrustProxy bool
	Checks     []Check
	Metrics    http.Handler
	Registerer prometheus.Registerer
	Logger     *slog.Logger
}

// route — маршрут: метод, шаблон пути и обработчик.
type route struct {
	method  string
	path    string
	handler http.HandlerFunc
}

type healthJSON struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// routes — все маршруты маркетплейса. Та же таблица сверяется с
// api/openapi.yaml в тестах: маршрут без описания — красный тест.
func routes(deps Deps) []route {
	catalog := &catalogHandlers{queries: deps.Catalog}
	taxonomy := &taxonomyHandlers{service: deps.Taxonomy, catalog: deps.Catalog}
	participants := &participantHandlers{service: deps.Participants}
	auth := &authHandlers{auth: deps.Auth, trustProxy: deps.TrustProxy}
	carts := &cartHandlers{carts: deps.Carts}
	shipping := &deliveryHandlers{directory: deps.Directory}
	checkouts := &checkoutHandlers{checkouts: deps.Checkouts}
	operator := func(next http.HandlerFunc) http.HandlerFunc { return operatorOnly(deps.OperatorToken, next) }
	signedIn := func(next http.HandlerFunc) http.HandlerFunc { return buyerOnly(deps.Auth, next) }

	metrics := func(w http.ResponseWriter, r *http.Request) { deps.Metrics.ServeHTTP(w, r) }

	return []route{
		{http.MethodGet, "/health", health(deps.Checks)},
		{http.MethodGet, "/metrics", metrics},

		{http.MethodGet, "/api/v1/taxonomy/categories", taxonomy.categories},
		{http.MethodGet, "/api/v1/taxonomy/categories/{id}/schema", taxonomy.schema},
		{http.MethodGet, "/api/v1/taxonomy/version", taxonomy.version},
		{http.MethodGet, "/api/v1/operator/taxonomy", operator(taxonomy.categories)},
		{http.MethodPut, "/api/v1/operator/taxonomy", operator(taxonomy.load)},
		{http.MethodGet, "/api/v1/operator/catalog/offers/{offerId}/publication", operator(taxonomy.publication)},

		{http.MethodGet, "/api/v1/catalog/offers", catalog.list},
		{http.MethodGet, "/api/v1/catalog/offers/{offerId}", catalog.card},

		{http.MethodGet, "/api/v1/operator/participants", operator(participants.list)},
		{http.MethodPost, "/api/v1/operator/participants", operator(participants.create)},
		{http.MethodGet, "/api/v1/operator/participants/{tenantId}", operator(participants.get)},
		{http.MethodPatch, "/api/v1/operator/participants/{tenantId}", operator(participants.rename)},
		{
			http.MethodPut,
			"/api/v1/operator/participants/{tenantId}/payment-account",
			operator(participants.setPaymentAccount),
		},
		{http.MethodPost, "/api/v1/operator/participants/{tenantId}/enable", operator(participants.enable)},
		{http.MethodPost, "/api/v1/operator/participants/{tenantId}/disable", operator(participants.disable)},

		{http.MethodPost, "/api/v1/auth/call", auth.start},
		{http.MethodPost, "/api/v1/auth/call/check", auth.check},
		{http.MethodPost, "/api/v1/auth/logout", auth.logout},
		{http.MethodPost, "/api/v1/auth/webhooks/smsru", auth.smsruWebhook},
		{http.MethodGet, "/api/v1/me", signedIn(auth.me)},

		{http.MethodGet, "/api/v1/cart", signedIn(carts.get)},
		{http.MethodPost, "/api/v1/cart/lines", signedIn(carts.add)},
		{http.MethodPut, "/api/v1/cart/lines/{offerId}", signedIn(carts.setQuantity)},
		{http.MethodDelete, "/api/v1/cart/lines/{offerId}", signedIn(carts.remove)},
		{http.MethodPost, "/api/v1/cart/verify", signedIn(carts.verify)},

		{http.MethodGet, "/api/v1/delivery/carriers", shipping.carriers},
		{http.MethodGet, "/api/v1/delivery/pickup-points", shipping.pickupPoints},

		{http.MethodPost, "/api/v1/checkout", signedIn(checkouts.create)},
		{http.MethodGet, "/api/v1/checkout/{checkoutId}", signedIn(checkouts.get)},
		{http.MethodPost, "/api/v1/checkout/{checkoutId}/delivery/quotes", signedIn(checkouts.quote)},
		{http.MethodPost, "/api/v1/checkout/{checkoutId}/confirm", signedIn(checkouts.confirm)},
	}
}

// NewHandler — маршруты маркетплейса.
func NewHandler(deps Deps) http.Handler {
	mux := http.NewServeMux()

	for _, r := range routes(deps) {
		mux.HandleFunc(r.method+" "+r.path, r.handler)
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { writeError(w, r, ErrRouteNotFound) })

	return observe(deps.Logger, newHTTPMetrics(deps.Registerer), mux)
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
				loggerFrom(r.Context()).Warn(
					"health: зависимость не отвечает",
					slog.String("check", check.Name),
					slog.Any("error", err),
				)

				continue
			}

			body.Checks[check.Name] = "ok"
		}

		writeJSON(w, status, body)
	}
}
