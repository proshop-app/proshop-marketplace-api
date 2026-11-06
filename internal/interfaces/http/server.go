// Package http — REST маркетплейса: каталог для витрины, операторские ручки
// участников и служебные адреса. Обработчики тонкие: разбор запроса, вызов
// сценария приложения, ответ.
package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	apppromotion "github.com/proshop-app/proshop-marketplace/internal/application/promotion"

	"github.com/prometheus/client_golang/prometheus"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	appoperator "github.com/proshop-app/proshop-marketplace/internal/application/operator"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
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
	Promotion      *apppromotion.Service
	ServerAPIToken string
	Moderation     *appmoderation.Service
	Taxonomy       *apptaxonomy.Service
	Catalog        *appcatalog.Queries
	Participants   *appparticipant.Service
	Auth           *appbuyer.Auth
	Carts          *appcart.Service
	Directory      *appdelivery.Directory
	Checkouts      *appcheckout.Service
	Reference      *appreference.Service
	OperatorToken  string
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
	promotion := &promotionHandlers{service: deps.Promotion, trustProxy: deps.TrustProxy}
	machine := func(next http.HandlerFunc) http.HandlerFunc { return serverAPI(deps.ServerAPIToken, next) }
	moderation := &moderationHandlers{service: deps.Moderation}
	catalog := &catalogHandlers{queries: deps.Catalog}
	taxonomy := &taxonomyHandlers{service: deps.Taxonomy, catalog: deps.Catalog}
	participants := &participantHandlers{service: deps.Participants}
	auth := &authHandlers{auth: deps.Auth, trustProxy: deps.TrustProxy}
	carts := &cartHandlers{carts: deps.Carts}
	shipping := &deliveryHandlers{directory: deps.Directory}
	checkouts := &checkoutHandlers{checkouts: deps.Checkouts}
	registry := newReferenceHandlers(deps.Reference, deps.Registerer)
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
		{http.MethodGet, "/api/v1/shops", participants.shops},
		{http.MethodGet, "/api/v1/shops/{slug}", participants.shop},
		{http.MethodGet, "/api/v1/internal/tenants/{tenantId}/store-card", machine(participants.storeCard)},
		{
			http.MethodPost,
			"/api/v1/internal/tenants/{tenantId}/settings/sync",
			machine(participants.syncSettings),
		},
		{
			http.MethodPost,
			"/api/v1/operator/participants/{tenantId}/card/hide",
			operator(participants.moderateCard),
		},
		{
			http.MethodDelete,
			"/api/v1/operator/participants/{tenantId}/card/hide/{key}",
			operator(participants.moderateCard),
		},
		{http.MethodGet, "/api/v1/internal/tenants/{tenantId}/participation", machine(participants.participation)},
		{
			http.MethodPost,
			"/api/v1/internal/tenants/{tenantId}/participation/submit",
			machine(participants.participation),
		},
		{http.MethodPost, "/api/v1/operator/participants/{tenantId}/return", operator(participants.returnParticipant)},
		{http.MethodGet, "/api/v1/operator/promotion/overview", operator(promotion.overview)},
		{http.MethodGet, "/api/v1/operator/promotion/campaigns", operator(promotion.list)},
		{http.MethodPost, "/api/v1/operator/promotion/campaigns", operator(promotion.create)},
		{http.MethodGet, "/api/v1/operator/promotion/campaigns/{id}", operator(promotion.campaign)},
		{http.MethodPatch, "/api/v1/operator/promotion/campaigns/{id}", operator(promotion.edit)},
		{http.MethodDelete, "/api/v1/operator/promotion/campaigns/{id}", operator(promotion.delete)},
		{http.MethodPost, "/api/v1/operator/promotion/campaigns/{id}/decisions", operator(promotion.decide)},
		{http.MethodGet, "/api/v1/operator/promotion/campaigns/{id}/decisions", operator(promotion.decisions)},
		{http.MethodGet, "/api/v1/operator/promotion/campaigns/{id}/stats", operator(promotion.stats)},
		{http.MethodGet, "/api/v1/operator/promotion/placements", operator(promotion.placements)},
		{http.MethodPut, "/api/v1/operator/promotion/placements/{code}", operator(promotion.configure)},
		{http.MethodGet, "/api/v1/operator/promotion/placements/{code}/calendar", operator(promotion.calendar)},
		{http.MethodGet, "/api/v1/internal/tenants/{tenantId}/promotion/placements", machine(promotion.placements)},
		{http.MethodGet, "/api/v1/internal/tenants/{tenantId}/promotion/availability", machine(promotion.availability)},
		{http.MethodGet, "/api/v1/internal/tenants/{tenantId}/promotion/campaigns", machine(promotion.list)},
		{http.MethodPost, "/api/v1/internal/tenants/{tenantId}/promotion/campaigns", machine(promotion.create)},
		{http.MethodGet, "/api/v1/internal/tenants/{tenantId}/promotion/campaigns/{id}", machine(promotion.campaign)},
		{http.MethodPatch, "/api/v1/internal/tenants/{tenantId}/promotion/campaigns/{id}", machine(promotion.edit)},
		{http.MethodDelete, "/api/v1/internal/tenants/{tenantId}/promotion/campaigns/{id}", machine(promotion.delete)},
		{
			http.MethodPost,
			"/api/v1/internal/tenants/{tenantId}/promotion/campaigns/{id}/submit",
			machine(promotion.decide),
		},
		{
			http.MethodPost,
			"/api/v1/internal/tenants/{tenantId}/promotion/campaigns/{id}/revise",
			machine(promotion.decide),
		},
		{
			http.MethodPost,
			"/api/v1/internal/tenants/{tenantId}/promotion/campaigns/{id}/stop",
			machine(promotion.decide),
		},
		{
			http.MethodGet,
			"/api/v1/internal/tenants/{tenantId}/promotion/campaigns/{id}/stats",
			machine(promotion.stats),
		},
		{http.MethodGet, "/api/v1/catalog/sponsored", optionalBuyer(deps.Auth, promotion.sponsored)},
		{http.MethodPost, "/api/v1/promotion/events", optionalBuyer(deps.Auth, promotion.events)},
		{http.MethodPut, "/api/v1/operator/participants/{tenantId}/trust", operator(participants.decision)},
		{http.MethodGet, "/api/v1/operator/participants/{tenantId}/decisions", operator(participants.decisions)},
		{http.MethodGet, "/api/v1/operator/moderation/cases", operator(moderation.cases)},
		{http.MethodPost, "/api/v1/operator/moderation/cases", operator(moderation.open)},
		{http.MethodGet, "/api/v1/operator/moderation/cases/{caseId}", operator(moderation.getCase)},
		{http.MethodPost, "/api/v1/operator/moderation/cases/{caseId}/allow", operator(moderation.decide)},
		{http.MethodPost, "/api/v1/operator/moderation/cases/{caseId}/reject", operator(moderation.decide)},
		{http.MethodGet, "/api/v1/operator/moderation/offers/{offerId}", operator(moderation.offer)},
		{http.MethodGet, "/api/v1/operator/moderation/verdicts", operator(moderation.verdicts)},
		{http.MethodGet, "/api/v1/operator/moderation/rules", operator(moderation.rules)},
		{http.MethodGet, "/api/v1/operator/moderation/stop-phrases", operator(moderation.phrases)},
		{http.MethodPost, "/api/v1/operator/moderation/stop-phrases", operator(moderation.addPhrase)},
		{http.MethodDelete, "/api/v1/operator/moderation/stop-phrases/{key}", operator(moderation.removePhrase)},
		{http.MethodGet, "/api/v1/operator/moderation/reasons", operator(moderation.reasons)},
		{http.MethodPut, "/api/v1/operator/moderation/reasons/{code}", operator(moderation.putReason)},
		{http.MethodPost, "/api/v1/operator/moderation/reasons", operator(moderation.addReason)},
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
		{http.MethodGet, "/api/v1/operator/taxonomy", operator(taxonomy.current)},
		{http.MethodPut, "/api/v1/operator/taxonomy/{part}", operator(taxonomy.load)},
		{http.MethodGet, "/api/v1/operator/taxonomy/{part}", operator(taxonomy.download)},
		{http.MethodGet, "/api/v1/taxonomy/attributes", taxonomy.attributes},
		{http.MethodGet, "/api/v1/taxonomy/schemas", taxonomy.schemas},
		{http.MethodGet, "/api/v1/operator/catalog/offers/{offerId}/publication", operator(taxonomy.publication)},

		{http.MethodGet, "/api/v1/recommendations", optionalBuyer(deps.Auth, catalog.recommendations)},
		{http.MethodGet, "/api/v1/catalog/offers/{offerId}/reviews", catalog.reviews},
		{http.MethodGet, "/api/v1/catalog/offers", optionalBuyer(deps.Auth, catalog.list)},
		{http.MethodGet, "/api/v1/catalog/offers/{offerId}", optionalBuyer(deps.Auth, catalog.card)},

		{http.MethodGet, "/api/v1/operator/participants", operator(participants.list)},
		{http.MethodPost, "/api/v1/operator/participants", operator(participants.create)},
		{http.MethodGet, "/api/v1/operator/participants/{tenantId}", operator(participants.get)},
		{http.MethodPost, "/api/v1/operator/participants/{tenantId}/enable", operator(participants.decision)},
		{http.MethodPost, "/api/v1/operator/participants/{tenantId}/disable", operator(participants.decision)},

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

		{http.MethodGet, "/internal/reference", reader(registry.discovery)},
		{http.MethodGet, "/api/v1/internal/reference", reader(registry.discovery)},
		{http.MethodGet, "/api/v1/internal/reference/{dict}/snapshot", reader(registry.snapshot)},
		{http.MethodGet, "/api/v1/internal/reference/{dict}/entries/{id}", reader(registry.get)},
		{http.MethodGet, "/internal/reference/{dict}/snapshot", reader(registry.snapshot)},
		{http.MethodPost, "/internal/reference/{dict}/resolve", reader(registry.resolve)},
		{http.MethodGet, "/internal/reference/{dict}/entries", reader(registry.list)},
		{http.MethodPost, "/internal/reference/{dict}/entries", operator(registry.create)},
		{http.MethodGet, "/internal/reference/{dict}/entries/{id}", reader(registry.get)},
		{http.MethodPatch, "/internal/reference/{dict}/entries/{id}", operator(registry.update)},
		{http.MethodPost, "/internal/reference/{dict}/entries/{id}/merge", operator(registry.merge)},
		{http.MethodPost, "/internal/reference/{dict}/entries/{id}/aliases", operator(registry.addAlias)},
		{http.MethodDelete, "/internal/reference/{dict}/aliases/{id}", operator(registry.removeAlias)},
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
