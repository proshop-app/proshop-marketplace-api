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
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appoperator "github.com/proshop-app/proshop-marketplace/internal/application/operator"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
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
	Reference     ports.ReferenceAPI
	OperatorToken string
	// ReferenceReadToken — токен читателя реестра: снимок, resolve и чтение записей без прав оператора.
	ReferenceReadToken string
	OperatorAuth       *appoperator.Auth
	Events             *messaging.Events
	OperatorOrigin     string
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
	registry := referenceHandlers{api: deps.Reference}
	gateway := &operatorHandlers{
		auth:       deps.OperatorAuth,
		events:     deps.Events,
		origin:     deps.OperatorOrigin,
		trustProxy: deps.TrustProxy,
	}
	operator := func(next http.HandlerFunc) http.HandlerFunc { return gateway.only(deps.OperatorToken, next) }
	signedIn := func(next http.HandlerFunc) http.HandlerFunc { return buyerOnly(deps.Auth, next) }
	reader := func(next http.HandlerFunc) http.HandlerFunc {
		return referenceReader(deps.ReferenceReadToken, operator(next), next)
	}
	metrics := func(w http.ResponseWriter, r *http.Request) { deps.Metrics.ServeHTTP(w, r) }

	return []route{
		{http.MethodPost, "/api/v1/operator/session", gateway.login},
		{http.MethodPost, "/api/v1/operator/session/refresh", gateway.refresh},
		{http.MethodGet, "/api/v1/operator/session", gateway.session},
		{http.MethodDelete, "/api/v1/operator/session", gateway.logout},
		{http.MethodGet, "/api/v1/operator/events", operator(gateway.listEvents)},
		{http.MethodPost, "/api/v1/operator/events/{eventId}/retry", operator(gateway.retry)},
		{http.MethodPost, "/api/v1/operator/events/{eventId}/resolve", operator(gateway.resolve)},
		{http.MethodPut, "/api/v1/operator/participants/{tenantId}/delivery", operator(participants.delivery)},
		{http.MethodGet, "/health", health(deps.Checks)},
		{http.MethodGet, "/metrics", metrics},

		{http.MethodGet, "/api/v1/taxonomy/categories", taxonomy.categories},
		{http.MethodGet, "/api/v1/taxonomy/categories/{id}/schema", taxonomy.schema},
		{http.MethodGet, "/api/v1/taxonomy/version", taxonomy.version},
		{http.MethodGet, "/api/v1/operator/taxonomy", operator(taxonomy.categories)},
		{http.MethodPut, "/api/v1/operator/taxonomy", operator(taxonomy.load)},
		{http.MethodGet, "/api/v1/operator/catalog/offers/{offerId}/publication", operator(taxonomy.publication)},

		{http.MethodGet, "/api/v1/catalog/offers", optionalBuyer(deps.Auth, catalog.list)},
		{http.MethodGet, "/api/v1/catalog/offers/{offerId}", optionalBuyer(deps.Auth, catalog.card)},

		{http.MethodGet, "/api/v1/operator/participants", operator(participants.list)},
		{http.MethodPost, "/api/v1/operator/participants", operator(participants.create)},
		{http.MethodGet, "/api/v1/operator/participants/{tenantId}", operator(participants.get)},
		{http.MethodPatch, "/api/v1/operator/participants/{tenantId}", operator(participants.rename)},
		{http.MethodPost, "/api/v1/operator/participants/{tenantId}/enable", operator(participants.enable)},
		{http.MethodPost, "/api/v1/operator/participants/{tenantId}/disable", operator(participants.disable)},

		{http.MethodPost, "/api/v1/auth/call", auth.start},
		{http.MethodPost, "/api/v1/auth/call/check", auth.check},
		{http.MethodPost, "/api/v1/auth/logout", auth.logout},
		{http.MethodPost, "/api/v1/auth/refresh", auth.refresh},
		{http.MethodPost, "/api/v1/auth/webhooks/smsru", auth.smsruWebhook},
		{http.MethodGet, "/api/v1/me", auth.me},

		{http.MethodGet, "/api/v1/cart", signedIn(carts.get)},
		{http.MethodPost, "/api/v1/cart/lines", signedIn(carts.add)},
		{http.MethodPut, "/api/v1/cart/lines/{offerId}", signedIn(carts.setQuantity)},
		{http.MethodDelete, "/api/v1/cart/lines/{offerId}", signedIn(carts.remove)},
		{http.MethodPost, "/api/v1/cart/verify", signedIn(carts.verify)},

		{http.MethodGet, "/api/v1/delivery/carriers", shipping.carriers},
		{http.MethodGet, "/api/v1/delivery/settings", checkouts.deliverySettings},
		{http.MethodGet, "/api/v1/delivery/pickup-points", shipping.pickupPoints},

		{http.MethodPost, "/api/v1/checkout", signedIn(checkouts.create)},
		{http.MethodGet, "/api/v1/checkout/{checkoutId}", signedIn(checkouts.get)},
		{http.MethodPost, "/api/v1/checkout/{checkoutId}/delivery/quotes", signedIn(checkouts.quote)},
		{http.MethodPost, "/api/v1/checkout/{checkoutId}/confirm", signedIn(checkouts.confirm)},
		{http.MethodPost, "/api/v1/checkout/{checkoutId}/orders", signedIn(checkouts.createOrders)},
		{http.MethodPost, "/api/v1/checkout/{checkoutId}/groups/{groupId}/pay", signedIn(checkouts.pay)},
		{http.MethodGet, "/api/v1/checkout/{checkoutId}/return", signedIn(checkouts.paymentReturn)},
		{http.MethodGet, "/api/v1/orders", signedIn(checkouts.orders)},
		{http.MethodGet, "/api/v1/operator/checkouts", operator(checkouts.operatorList)},

		{http.MethodGet, "/internal/reference/{dict}/snapshot", reader(registry.handler("snapshot", false))},
		{http.MethodPost, "/internal/reference/{dict}/resolve", reader(registry.handler("resolve", false))},
		{http.MethodGet, "/internal/reference/{dict}/entries", reader(registry.handler("entries", false))},
		{http.MethodPost, "/internal/reference/{dict}/entries", operator(registry.handler("entries", true))},
		{http.MethodGet, "/internal/reference/{dict}/entries/{id}", reader(registry.handler("entries/{id}", false))},
		{http.MethodPatch, "/internal/reference/{dict}/entries/{id}", operator(registry.handler("entries/{id}", true))},
		{
			http.MethodPost,
			"/internal/reference/{dict}/entries/{id}/merge",
			operator(registry.handler("entries/{id}/merge", true)),
		},
		{
			http.MethodPost,
			"/internal/reference/{dict}/entries/{id}/aliases",
			operator(registry.handler("entries/{id}/aliases", true)),
		},
		{
			http.MethodDelete,
			"/internal/reference/{dict}/aliases/{id}",
			operator(registry.handler("aliases/{id}", true)),
		},
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
