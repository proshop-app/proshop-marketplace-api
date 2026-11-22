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
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	appoperator "github.com/proshop-app/proshop-marketplace/internal/application/operator"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apppromotion "github.com/proshop-app/proshop-marketplace/internal/application/promotion"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/application/searchfeedback"
	appsynonyms "github.com/proshop-app/proshop-marketplace/internal/application/synonyms"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
)

// Check — проверка зависимости; Optional не закрывает готовность при частичном отказе.
type Check struct {
	Optional bool
	Name     string
	Probe    func(ctx context.Context) error
}

// Deps — сценарии и служебное, из которых собираются маршруты.
type Deps struct {
	Cities                 *appbuyer.Cities
	DatabaseBudgets        ports.DatabaseBudgets
	RequestTimeout         time.Duration
	ReferenceImportTimeout time.Duration
	Statuses               *moderationstatus.Service
	Features               *appbuyer.Features
	Promotion              *apppromotion.Service
	ServerAPIToken         string
	Moderation             *appmoderation.Service
	Taxonomy               *apptaxonomy.Service
	SellersCounts          ports.SellersCounts
	Catalog                *appcatalog.Queries
	SearchFeedback         *searchfeedback.Feedback
	SearchBuyerKeys        *searchfeedback.BuyerKeys
	Participants           *appparticipant.Service
	Auth                   *appbuyer.Auth
	Carts                  *appcart.Service
	Directory              *appdelivery.Service
	Checkouts              *appcheckout.Service
	Reference              *appreference.Service
	Synonyms               *appsynonyms.Service
	OperatorToken          string
	// ReferenceReadToken — токен читателя реестра: снимок, resolve и чтение записей без прав оператора.
	ReferenceReadToken string
	OperatorAuth       *appoperator.Auth
	Dispatcher         *messaging.Dispatcher
	Events             *messaging.Events
	OperatorOrigin     string
	// TrustedProxies — сколько прокси перед сервисом дописывают X-Forwarded-For; 0 — заголовок не читается.
	TrustedProxies int
	Checks         []Check
	Metrics        http.Handler
	Registerer     prometheus.Registerer
	Logger         *slog.Logger
}

// route — маршрут: класс срока, метод, шаблон пути и обработчик.
type route struct {
	class   requestClass
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
	cities := &cityHandlers{service: deps.Cities, trustedProxies: deps.TrustedProxies}
	promotion := &promotionHandlers{service: deps.Promotion, trustedProxies: deps.TrustedProxies}
	machine := func(next http.HandlerFunc) http.HandlerFunc { return serverAPI(deps.ServerAPIToken, next) }
	moderation := &moderationHandlers{service: deps.Moderation}
	search := &searchHandlers{
		feedback:       deps.SearchFeedback,
		keys:           deps.SearchBuyerKeys,
		auth:           deps.Auth,
		trustedProxies: deps.TrustedProxies,
	}
	catalog := &catalogHandlers{queries: deps.Catalog, sellers: deps.SellersCounts, search: search}
	taxonomy := &taxonomyHandlers{service: deps.Taxonomy, catalog: deps.Catalog}
	participants := &participantHandlers{service: deps.Participants, buyerFeatures: deps.Features}
	statuses := &statusHandlers{service: deps.Statuses}
	features := &buyerFeatureHandlers{
		service:        deps.Features,
		trustedProxies: deps.TrustedProxies,
	}
	auth := &authHandlers{auth: deps.Auth, trustedProxies: deps.TrustedProxies}
	carts := &cartHandlers{carts: deps.Carts}
	shipping := &deliveryHandlers{directory: deps.Directory}
	checkouts := &checkoutHandlers{checkouts: deps.Checkouts}
	registry := newReferenceHandlers(deps.Reference, deps.Registerer)
	synonyms := synonymHandlers{service: deps.Synonyms}
	gateway := &operatorHandlers{
		auth:           deps.OperatorAuth,
		events:         deps.Events,
		dispatcher:     deps.Dispatcher,
		origin:         deps.OperatorOrigin,
		trustedProxies: deps.TrustedProxies,
	}
	operator := func(next http.HandlerFunc) http.HandlerFunc { return gateway.only(deps.OperatorToken, next) }
	signedIn := func(next http.HandlerFunc) http.HandlerFunc { return buyerOnly(deps.Auth, next) }
	reader := func(next http.HandlerFunc) http.HandlerFunc {
		return referenceReader(deps.ReferenceReadToken, operator(next), next)
	}
	metrics := func(w http.ResponseWriter, r *http.Request) { deps.Metrics.ServeHTTP(w, r) }

	return []route{
		{readRequest, "GET", "/api/v1/city", cities.detect},
		{readRequest, "GET", "/api/v1/delivery/cities", cities.suggest},
		{readRequest, http.MethodGet, "/api/v1/buyer/profile", privateBuyer(signedIn(features.profile))},
		{
			ingestionRequest,
			http.MethodPatch,
			"/api/v1/buyer/profile",
			privateBuyer(signedIn(features.patchProfile)),
		},
		{readRequest, http.MethodGet, "/api/v1/buyer/favorites", privateBuyer(signedIn(features.favorites))},
		{
			ingestionRequest,
			http.MethodPut,
			"/api/v1/buyer/favorites/{offerId}",
			privateBuyer(signedIn(features.putSaved(ports.Favorites))),
		},
		{
			ingestionRequest,
			http.MethodDelete,
			"/api/v1/buyer/favorites/{offerId}",
			privateBuyer(signedIn(features.deleteSaved(ports.Favorites))),
		},
		{
			readRequest,
			http.MethodGet,
			"/api/v1/buyer/price-watches",
			privateBuyer(signedIn(features.watches)),
		},
		{
			ingestionRequest,
			http.MethodPut,
			"/api/v1/buyer/price-watches/{offerId}",
			privateBuyer(signedIn(features.putSaved(ports.PriceWatches))),
		},
		{
			ingestionRequest,
			http.MethodDelete,
			"/api/v1/buyer/price-watches/{offerId}",
			privateBuyer(signedIn(features.deleteSaved(ports.PriceWatches))),
		},
		{
			readRequest,
			http.MethodGet,
			"/api/v1/buyer/subscriptions/shops",
			privateBuyer(signedIn(features.subscriptions)),
		},
		{
			ingestionRequest,
			http.MethodPut,
			"/api/v1/buyer/subscriptions/shops/{tenantId}",
			privateBuyer(signedIn(features.putSubscription)),
		},
		{
			ingestionRequest,
			http.MethodDelete,
			"/api/v1/buyer/subscriptions/shops/{tenantId}",
			privateBuyer(signedIn(features.deleteSubscription)),
		},
		{readRequest, http.MethodGet, "/api/v1/region", publicBuyer(deps.Auth, features.region)},

		{readRequest, http.MethodGet, "/api/v1/shops", participants.shops},
		{readRequest, http.MethodGet, "/api/v1/shops/{slug}", publicBuyer(deps.Auth, participants.shop)},
		{
			readRequest,
			http.MethodGet,
			"/api/v1/internal/tenants/{tenantId}/store-card",
			machine(participants.storeCard),
		},
		{
			ingestionRequest,
			http.MethodPost,
			"/api/v1/internal/tenants/{tenantId}/settings/sync",
			machine(participants.syncSettings),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/participants/{tenantId}/card/hide",
			operator(participants.moderateCard),
		},
		{
			backgroundRequest,
			http.MethodDelete,
			"/api/v1/operator/participants/{tenantId}/card/hide/{key}",
			operator(participants.moderateCard),
		},
		{
			readRequest,
			http.MethodGet,
			"/api/v1/internal/tenants/{tenantId}/participation",
			machine(participants.participation),
		},
		{readRequest, http.MethodGet, "/api/v1/internal/tenants/{tenantId}/status", machine(statuses.get)},
		{
			ingestionRequest,
			http.MethodPost,
			"/api/v1/internal/tenants/{tenantId}/participation/submit",
			machine(participants.participation),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/participants/{tenantId}/return",
			operator(participants.returnParticipant),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/promotion/overview",
			operator(promotion.overview),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/promotion/campaigns",
			operator(promotion.list),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/promotion/campaigns",
			operator(promotion.create),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/promotion/campaigns/{id}",
			operator(promotion.campaign),
		},
		{
			backgroundRequest,
			http.MethodPatch,
			"/api/v1/operator/promotion/campaigns/{id}",
			operator(promotion.edit),
		},
		{
			backgroundRequest,
			http.MethodDelete,
			"/api/v1/operator/promotion/campaigns/{id}",
			operator(promotion.delete),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/promotion/campaigns/{id}/decisions",
			operator(promotion.decide),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/promotion/campaigns/{id}/decisions",
			operator(promotion.decisions),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/promotion/campaigns/{id}/stats",
			operator(promotion.stats),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/promotion/placements",
			operator(promotion.placements),
		},
		{
			backgroundRequest,
			http.MethodPut,
			"/api/v1/operator/promotion/placements/{code}",
			operator(promotion.configure),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/promotion/placements/{code}/calendar",
			operator(promotion.calendar),
		},
		{
			readRequest,
			http.MethodGet,
			"/api/v1/internal/tenants/{tenantId}/promotion/placements",
			machine(promotion.placements),
		},
		{
			readRequest,
			http.MethodGet,
			"/api/v1/internal/tenants/{tenantId}/promotion/availability",
			machine(promotion.availability),
		},
		{
			readRequest,
			http.MethodGet,
			"/api/v1/internal/tenants/{tenantId}/promotion/campaigns",
			machine(promotion.list),
		},
		{
			ingestionRequest,
			http.MethodPost,
			"/api/v1/internal/tenants/{tenantId}/promotion/campaigns",
			machine(promotion.create),
		},
		{
			readRequest,
			http.MethodGet,
			"/api/v1/internal/tenants/{tenantId}/promotion/campaigns/{id}",
			machine(promotion.campaign),
		},
		{
			ingestionRequest,
			http.MethodPatch,
			"/api/v1/internal/tenants/{tenantId}/promotion/campaigns/{id}",
			machine(promotion.edit),
		},
		{
			ingestionRequest,
			http.MethodDelete,
			"/api/v1/internal/tenants/{tenantId}/promotion/campaigns/{id}",
			machine(promotion.delete),
		},
		{
			ingestionRequest,
			http.MethodPost,
			"/api/v1/internal/tenants/{tenantId}/promotion/campaigns/{id}/submit",
			machine(promotion.decide),
		},
		{
			ingestionRequest,
			http.MethodPost,
			"/api/v1/internal/tenants/{tenantId}/promotion/campaigns/{id}/revise",
			machine(promotion.decide),
		},
		{
			ingestionRequest,
			http.MethodPost,
			"/api/v1/internal/tenants/{tenantId}/promotion/campaigns/{id}/stop",
			machine(promotion.decide),
		},
		{
			readRequest,
			http.MethodGet,
			"/api/v1/internal/tenants/{tenantId}/promotion/campaigns/{id}/stats",
			machine(promotion.stats),
		},
		{
			readRequest,
			http.MethodGet,
			"/api/v1/catalog/sponsored",
			promotion.sponsored,
		},
		{
			ingestionRequest,
			http.MethodPost,
			"/api/v1/promotion/events",
			publicBuyer(deps.Auth, promotion.events),
		},
		{
			backgroundRequest,
			http.MethodPut,
			"/api/v1/operator/participants/{tenantId}/trust",
			operator(participants.decision),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/participants/{tenantId}/decisions",
			operator(participants.decisions),
		},
		{backgroundRequest, http.MethodGet, "/api/v1/operator/moderation/cases", operator(moderation.cases)},
		{backgroundRequest, http.MethodPost, "/api/v1/operator/moderation/cases", operator(moderation.open)},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/moderation/cases/bulk-allow",
			operator(moderation.bulkAllow),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/moderation/cases/bulk-count",
			operator(moderation.bulkCount),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/moderation/cases/{caseId}",
			operator(moderation.getCase),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/moderation/cases/{caseId}/allow",
			operator(moderation.decide),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/moderation/cases/{caseId}/reject",
			operator(moderation.decide),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/moderation/offers/{offerId}",
			operator(moderation.offer),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/moderation/verdicts",
			operator(moderation.verdicts),
		},
		{backgroundRequest, http.MethodGet, "/api/v1/operator/moderation/rules", operator(moderation.rules)},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/moderation/stop-phrases",
			operator(moderation.phrases),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/moderation/stop-phrases",
			operator(moderation.addPhrase),
		},
		{
			backgroundRequest,
			http.MethodDelete,
			"/api/v1/operator/moderation/stop-phrases/{key}",
			operator(moderation.removePhrase),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/moderation/reasons",
			operator(moderation.reasons),
		},
		{
			backgroundRequest,
			http.MethodPut,
			"/api/v1/operator/moderation/reasons/{code}",
			operator(moderation.putReason),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/moderation/reasons",
			operator(moderation.addReason),
		},
		{backgroundRequest, http.MethodPost, "/api/v1/operator/session", gateway.login},
		{backgroundRequest, http.MethodPost, "/api/v1/operator/session/refresh", gateway.refresh},
		{backgroundRequest, http.MethodGet, "/api/v1/operator/session", gateway.session},
		{backgroundRequest, http.MethodDelete, "/api/v1/operator/session", gateway.logout},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/outbox/failures",
			operator(gateway.outboxFailures),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/outbox/failures/retry",
			operator(gateway.requeueTasks),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/outbox/failures/{taskId}/retry",
			operator(gateway.requeueTask),
		},
		{backgroundRequest, http.MethodGet, "/api/v1/operator/events", operator(gateway.listEvents)},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/events/{eventId}/retry",
			operator(gateway.retry),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/events/{eventId}/resolve",
			operator(gateway.resolve),
		},
		{
			backgroundRequest,
			http.MethodPut,
			"/api/v1/operator/participants/{tenantId}/delivery",
			operator(participants.delivery),
		},
		{readRequest, http.MethodGet, "/live", health(nil, false)},
		{readRequest, http.MethodGet, "/ready", health(deps.Checks, true)},
		{readRequest, http.MethodGet, "/health", health(deps.Checks, false)},
		{readRequest, http.MethodGet, "/metrics", metrics},

		{readRequest, http.MethodGet, "/api/v1/taxonomy/dictionary-bindings", taxonomy.dictionaryBindings},
		{readRequest, http.MethodGet, "/api/v1/taxonomy/dictionary-sources", taxonomy.dictionarySources},
		{
			backgroundRequest,
			http.MethodPut,
			"/api/v1/operator/taxonomy/dictionary-bindings/{attributeCode}",
			operator(taxonomy.putDictionaryBinding),
		},
		{
			backgroundRequest,
			http.MethodDelete,
			"/api/v1/operator/taxonomy/dictionary-bindings/{attributeCode}",
			operator(taxonomy.deleteDictionaryBinding),
		},
		{readRequest, http.MethodGet, "/api/v1/taxonomy/categories", taxonomy.categories},
		{readRequest, http.MethodGet, "/api/v1/taxonomy/categories/{id}/schema", taxonomy.schema},
		{readRequest, http.MethodGet, "/api/v1/taxonomy/version", taxonomy.version},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/taxonomy/revalidation-failures",
			operator(taxonomy.revalidationFailures),
		},
		{backgroundRequest, http.MethodGet, "/api/v1/operator/taxonomy", operator(taxonomy.current)},
		{backgroundRequest, http.MethodPut, "/api/v1/operator/taxonomy/{part}", operator(taxonomy.load)},
		{backgroundRequest, http.MethodGet, "/api/v1/operator/taxonomy/{part}", operator(taxonomy.download)},
		{readRequest, http.MethodGet, "/api/v1/taxonomy/attributes", taxonomy.attributes},
		{readRequest, http.MethodGet, "/api/v1/taxonomy/schemas", taxonomy.schemas},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/catalog/offers/{offerId}/publication",
			operator(taxonomy.publication),
		},

		{
			readRequest,
			http.MethodGet,
			"/api/v1/recommendations",
			publicBuyer(deps.Auth, catalog.recommendations),
		},
		{readRequest, http.MethodGet, "/api/v1/catalog/offers/{offerId}/reviews", catalog.reviews},
		{readRequest, http.MethodGet, "/api/v1/catalog/offers", catalog.list},
		{ingestionRequest, http.MethodPost, "/api/v1/search/feedback", search.record},
		{
			readRequest,
			http.MethodGet,
			"/api/v1/catalog/offers/{offerId}",
			catalog.card,
		},
		{
			readRequest,
			http.MethodGet,
			"/api/v1/catalog/variants/{tenantId}/{groupId}",
			catalog.variants,
		},

		{backgroundRequest, http.MethodGet, "/api/v1/operator/participants", operator(participants.list)},
		{backgroundRequest, http.MethodPost, "/api/v1/operator/participants", operator(participants.create)},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/participants/{tenantId}",
			operator(participants.get),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/participants/{tenantId}/enable",
			operator(participants.decision),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/participants/{tenantId}/disable",
			operator(participants.decision),
		},

		{ingestionRequest, http.MethodPost, "/api/v1/auth/call", auth.start},
		{ingestionRequest, http.MethodPost, "/api/v1/auth/call/check", auth.check},
		{ingestionRequest, http.MethodPost, "/api/v1/auth/logout", auth.logout},
		{ingestionRequest, http.MethodPost, "/api/v1/auth/refresh", auth.refresh},
		{ingestionRequest, http.MethodPost, "/api/v1/auth/webhooks/smsru", auth.smsruWebhook},
		{readRequest, http.MethodGet, "/api/v1/me", auth.me},

		{checkoutRequest, http.MethodGet, "/api/v1/cart", signedIn(carts.get)},
		{checkoutRequest, http.MethodPost, "/api/v1/cart/lines", signedIn(carts.add)},
		{checkoutRequest, http.MethodPut, "/api/v1/cart/lines/{offerId}", signedIn(carts.setQuantity)},
		{checkoutRequest, http.MethodDelete, "/api/v1/cart/lines/{offerId}", signedIn(carts.remove)},
		{checkoutRequest, http.MethodPost, "/api/v1/cart/verify", signedIn(carts.verify)},
		{
			checkoutRequest,
			http.MethodPut,
			"/api/v1/cart/groups/{tenantId}/promo-code",
			signedIn(carts.promo),
		},
		{
			checkoutRequest,
			http.MethodDelete,
			"/api/v1/cart/groups/{tenantId}/promo-code",
			signedIn(carts.promo),
		},

		{readRequest, http.MethodGet, "/api/v1/delivery/carriers", shipping.carriers},
		{readRequest, http.MethodGet, "/api/v1/delivery/pickup-points", shipping.pickupPoints},

		{checkoutRequest, http.MethodPost, "/api/v1/checkout", signedIn(checkouts.create)},
		{checkoutRequest, http.MethodGet, "/api/v1/checkout/{checkoutId}", signedIn(checkouts.get)},
		{
			checkoutRequest,
			http.MethodGet,
			"/api/v1/checkout/{checkoutId}/delivery/pickup-points",
			signedIn(shipping.checkoutPickupPoints),
		},
		{
			checkoutRequest,
			http.MethodPost,
			"/api/v1/checkout/{checkoutId}/delivery/quotes",
			signedIn(checkouts.quote),
		},
		{
			checkoutRequest,
			http.MethodPost,
			"/api/v1/checkout/{checkoutId}/confirm",
			signedIn(checkouts.confirm),
		},
		{
			checkoutRequest,
			http.MethodPost,
			"/api/v1/checkout/{checkoutId}/orders",
			signedIn(checkouts.createOrders),
		},
		{
			checkoutRequest,
			http.MethodPost,
			"/api/v1/checkout/{checkoutId}/cancel",
			signedIn(checkouts.cancel),
		},
		{
			checkoutRequest,
			http.MethodPost,
			"/api/v1/checkout/{checkoutId}/groups/{tenantId}/remove",
			signedIn(checkouts.removeGroup),
		},
		{checkoutRequest, http.MethodPost, "/api/v1/checkout/{checkoutId}/pay", signedIn(checkouts.pay)},
		{
			checkoutRequest,
			http.MethodGet,
			"/api/v1/payment-stub/{checkoutId}",
			checkouts.paymentBrowser(signedIn(checkouts.paymentStub)),
		},
		{
			checkoutRequest,
			http.MethodPost,
			"/api/v1/payment-stub/{checkoutId}/confirm",
			checkouts.paymentBrowser(signedIn(checkouts.confirmPayment)),
		},
		{
			checkoutRequest,
			http.MethodPost,
			"/api/v1/payment-stub/{checkoutId}/cancel",
			checkouts.paymentBrowser(signedIn(checkouts.cancelPayment)),
		},
		{
			checkoutRequest,
			http.MethodGet,
			"/api/v1/checkout/{checkoutId}/return",
			signedIn(checkouts.paymentReturn),
		},
		{readRequest, http.MethodGet, "/api/v1/orders", signedIn(checkouts.orders)},
		{backgroundRequest, http.MethodGet, "/api/v1/operator/checkouts", operator(checkouts.operatorList)},

		{backgroundRequest, http.MethodGet, synonymSnapshotPath, reader(synonyms.snapshot)},
		{backgroundRequest, http.MethodGet, operatorSynonymPath + "/rules", operator(synonyms.list)},
		{backgroundRequest, http.MethodPatch, operatorSynonymPath + "/rules/{id}", operator(synonyms.disable)},
		{backgroundRequest, http.MethodGet, operatorSynonymPath + "/stop-list", operator(synonyms.stopList)},
		{backgroundRequest, http.MethodPut, operatorSynonymPath + "/stop-list", operator(synonyms.replaceStopList)},
		{backgroundRequest, http.MethodGet, "/api/v1/operator/reference", operator(registry.summaries)},
		{
			importRequest,
			http.MethodPost,
			"/api/v1/operator/reference/import",
			operator(referenceImportBudget(registry.importPackage)),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/reference/{dict}/entries",
			operator(registry.list),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/api/v1/operator/reference/{dict}/entries/review",
			operator(registry.review),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/operator/reference/{dict}/entries/{id}",
			operator(registry.get),
		},
		{
			backgroundRequest,
			http.MethodPatch,
			"/api/v1/operator/reference/{dict}/entries/{id}",
			operator(registry.update),
		},
		{backgroundRequest, http.MethodGet, "/internal/reference", reader(registry.discovery)},
		{backgroundRequest, http.MethodGet, "/api/v1/internal/reference", reader(registry.discovery)},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/internal/reference/{dict}/snapshot",
			reader(registry.snapshot),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/api/v1/internal/reference/{dict}/entries/{id}",
			reader(registry.get),
		},
		{
			backgroundRequest,
			http.MethodGet,
			"/internal/reference/{dict}/snapshot",
			reader(registry.snapshot),
		},
		{backgroundRequest, http.MethodPost, "/internal/reference/{dict}/resolve", reader(registry.resolve)},
		{backgroundRequest, http.MethodGet, "/internal/reference/{dict}/entries", reader(registry.list)},
		{
			backgroundRequest,
			http.MethodPost,
			"/internal/reference/{dict}/entries",
			operator(registry.create),
		},
		{backgroundRequest, http.MethodGet, "/internal/reference/{dict}/entries/{id}", reader(registry.get)},
		{
			backgroundRequest,
			http.MethodPatch,
			"/internal/reference/{dict}/entries/{id}",
			operator(registry.update),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/internal/reference/{dict}/entries/{id}/merge",
			operator(registry.merge),
		},
		{
			backgroundRequest,
			http.MethodPost,
			"/internal/reference/{dict}/entries/{id}/aliases",
			operator(registry.addAlias),
		},
		{
			backgroundRequest,
			http.MethodDelete,
			"/internal/reference/{dict}/aliases/{id}",
			operator(registry.removeAlias),
		},
	}
}

// NewHandler — маршруты маркетплейса.
func NewHandler(deps Deps) http.Handler {
	if deps.RequestTimeout <= 0 || deps.ReferenceImportTimeout <= 0 {
		panic("сроки HTTP должны быть положительными")
	}
	mux := http.NewServeMux()

	for _, r := range routes(deps) {
		mux.HandleFunc(r.method+" "+r.path, requestBudget(r.class, deps, moneyContract(r.path, r.handler)))
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { writeError(w, r, ErrRouteNotFound) })

	return observe(deps.Logger, newHTTPMetrics(deps.Registerer), mux)
}
