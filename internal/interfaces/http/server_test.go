package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const token = "operator-token-for-tests-0123456789"

var t0 = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

type env struct {
	server   *httptest.Server
	store    *fake.Store
	index    *fake.Index
	clock    *fake.Clock
	calls    *fake.Calls
	platform *fake.Platform
	pickups  *fake.PickupIndex
	healthy  error
}

type response struct {
	status int
	header http.Header
	body   map[string]any
}

func newEnv(t *testing.T) *env {
	t.Helper()

	e := &env{
		store:    fake.NewStore(),
		index:    fake.NewIndex(),
		clock:    fake.NewClock(t0),
		calls:    fake.NewCalls(),
		platform: fake.NewPlatform(),
		pickups:  fake.NewPickupIndex(),
	}
	e.store.SeedTaxonomy(taxonomy.Definition{
		Version:    1,
		Categories: []taxonomy.Category{{ID: "c1", Name: "Категория"}},
	})
	registry := prometheus.NewRegistry()
	identities := &fake.IDs{}
	provider := &fake.PickupProvider{Info: delivery.CarrierInfo{Code: "cdek", Name: "СДЭК"}}
	directory := appdelivery.NewDirectory(
		e.store,
		[]ports.PickupPointsProvider{provider},
		e.pickups,
		e.store,
		e.clock,
		"pod",
	)

	handler := transport.NewHandler(transport.Deps{
		Taxonomy:     apptaxonomy.NewService(e.store, e.clock),
		Catalog:      appcatalog.NewQueries(e.store, e.index),
		Participants: appparticipant.NewService(e.store, e.clock),
		Auth: appbuyer.NewAuth(
			e.store,
			e.calls,
			fake.NewSessions(),
			fake.NewLimiter(),
			identities,
			e.clock,
			appbuyer.AuthConfig{
				ChallengeTTL:  5 * time.Minute,
				CheckInterval: time.Second,
				SessionTTL:    time.Hour,
				PhoneLimit: appbuyer.Limit{
					Count:  10,
					Window: time.Minute,
				},
				IPLimit: appbuyer.Limit{Count: 10, Window: time.Minute},
			},
		),
		Carts:     appcart.NewService(e.store, e.index, e.platform, identities, e.clock, time.Hour),
		Directory: directory,
		Checkouts: appcheckout.NewService(
			e.store,
			e.index,
			e.platform,
			directory,
			identities,
			e.clock,
			appcheckout.Config{QuoteTTL: 30 * time.Minute, QuoteTimeout: time.Second},
		),
		OperatorToken: token,
		Checks:        []transport.Check{{Name: "postgres", Probe: func(context.Context) error { return e.healthy }}},
		Metrics:       promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
		Registerer:    registry,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	e.server = httptest.NewServer(handler)
	t.Cleanup(e.server.Close)

	return e
}

func (e *env) seedTenant(id ids.TenantID, slug string) {
	e.store.SeedTenant(participant.TenantState{
		TenantID:  id,
		Slug:      slug,
		Status:    participant.TenantActive,
		EventAt:   t0,
		Version:   1,
		UpdatedAt: t0,
	})
}

func (e *env) seedOffer(t *testing.T, tenant ids.TenantID, product ids.ProductID, price int64, tags ...string) {
	t.Helper()

	snapshot := catalog.Snapshot{
		TenantID:  tenant,
		ProductID: product,
		Revision:  1,
		At:        t0,
		Available: 2,
		Price:     money.FromKopecks(price),
		Card: catalog.Card{
			Marketplace: &taxonomy.Card{CategoryID: "c1", Published: true},
			Publication: taxonomy.Publication{
				Published:    true,
				Version:      1,
				CategoryID:   "c1",
				CategoryName: "Категория",
				CategoryPath: []string{"c1"},
			},
			Name:        "Товар " + string(product),
			Description: "Описание",
			Visible:     true,
			Tags:        tags,
			Categories:  []catalog.Category{{ID: "c1", Name: "Категория"}},
		},
	}

	p, err := catalog.NewProduct(snapshot, t0)
	if err != nil {
		t.Fatal(err)
	}

	e.store.SeedProduct(p.State())

	err = appcatalog.NewIndexer(e.store, e.index, e.clock, 100).SyncTenant(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) call(t *testing.T, method, path, body string, authorized bool) response {
	t.Helper()

	request, err := http.NewRequest(method, e.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if authorized {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("X-Request-Id", "req-1")

	raw, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Body.Close() }()

	out := response{status: raw.StatusCode, header: raw.Header}
	data, _ := io.ReadAll(raw.Body)
	if len(data) > 0 && strings.HasPrefix(raw.Header.Get("Content-Type"), "application/json") {
		out.body = decode(t, data)
	}

	return out
}

func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()

	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("ответ не JSON: %s", data)
	}

	return body
}

func errorCode(r response) string {
	detail, _ := r.body["error"].(map[string]any)
	code, _ := detail["code"].(string)

	return code
}

func TestHealth(t *testing.T) {
	e := newEnv(t)

	if r := e.call(t, http.MethodGet, "/health", "", false); r.status != http.StatusOK || r.body["status"] != "ok" {
		t.Fatalf("здоров: %+v", r)
	}

	e.healthy = errors.New("нет связи")
	r := e.call(t, http.MethodGet, "/health", "", false)
	checks, _ := r.body["checks"].(map[string]any)
	if r.status != http.StatusServiceUnavailable || checks["postgres"] != "fail" {
		t.Fatalf("зависимость не отвечает: %+v", r)
	}
}

func TestOperatorFlow(t *testing.T) {
	e := newEnv(t)
	e.seedTenant("t1", "shop-one")

	r := e.call(t, http.MethodGet, "/api/v1/operator/participants", "", false)
	if r.status != http.StatusUnauthorized || errorCode(r) != "http.unauthorized" {
		t.Fatalf("без токена: %+v", r)
	}

	r = e.call(t, http.MethodPost, "/api/v1/operator/participants", `{"tenantId":"t1","name":"Магазин"}`, true)
	if r.status != http.StatusCreated || r.body["slug"] != "shop-one" || r.body["status"] != "onboarding" ||
		r.body["paymentAccountId"] != nil {
		t.Fatalf("создание: %+v", r)
	}

	r = e.call(t, http.MethodPost, "/api/v1/operator/participants", `{"tenantId":"t1","name":"Ещё"}`, true)
	if r.status != http.StatusConflict || errorCode(r) != "participant.exists" {
		t.Fatalf("повтор: %+v", r)
	}

	r = e.call(t, http.MethodPost, "/api/v1/operator/participants", `{"tenantId":"t9","name":"x"}`, true)
	if r.status != http.StatusBadRequest || errorCode(r) != "participant.tenant_unknown" {
		t.Fatalf("нет в реестре: %+v", r)
	}

	r = e.call(t, http.MethodPost, "/api/v1/operator/participants", `{"tenantId":"t1","name":"x","extra":1}`, true)
	if r.status != http.StatusBadRequest || errorCode(r) != "http.invalid_body" {
		t.Fatalf("лишнее поле: %+v", r)
	}

	r = e.call(t, http.MethodPatch, "/api/v1/operator/participants/t1", `{"name":"Новое имя"}`, true)
	if r.status != http.StatusOK || r.body["name"] != "Новое имя" {
		t.Fatalf("переименование: %+v", r)
	}

	paymentAccount := "/api/v1/operator/participants/t1/payment-account"

	r = e.call(t, http.MethodPut, paymentAccount, `{"paymentAccountId":"506751"}`, true)
	if r.status != http.StatusOK || r.body["status"] != "connected" || r.body["paymentAccountId"] != "506751" {
		t.Fatalf("касса продавца: %+v", r)
	}

	r = e.call(t, http.MethodPut, paymentAccount, `{"paymentAccountId":"bad id"}`, true)
	if r.status != http.StatusBadRequest {
		t.Fatalf("некорректный идентификатор кассы: %+v", r)
	}

	r = e.call(t, http.MethodPost, "/api/v1/operator/participants/t1/enable", "", true)
	if r.status != http.StatusOK || r.body["enabled"] != true || r.body["shopEnabled"] != false ||
		r.body["visible"] != false {
		t.Fatalf("включение оператором без согласия магазина: %+v", r)
	}

	r = e.call(t, http.MethodPost, "/api/v1/operator/participants/t1/disable", "", true)
	if r.status != http.StatusOK || r.body["visible"] != false {
		t.Fatalf("выключение: %+v", r)
	}

	r = e.call(t, http.MethodGet, "/api/v1/operator/participants?limit=10", "", true)
	items, _ := r.body["items"].([]any)
	if r.status != http.StatusOK || len(items) != 1 || r.body["total"] != float64(1) {
		t.Fatalf("список: %+v", r)
	}

	r = e.call(t, http.MethodGet, "/api/v1/operator/participants?limit=x", "", true)
	if r.status != http.StatusBadRequest || errorCode(r) != "http.invalid_query" {
		t.Fatalf("лимит не число: %+v", r)
	}

	r = e.call(t, http.MethodGet, "/api/v1/operator/participants/t2", "", true)
	if r.status != http.StatusNotFound || errorCode(r) != "participant.not_found" {
		t.Fatalf("нет участника: %+v", r)
	}

	r = e.call(t, http.MethodGet, "/api/v1/operator/participants/bad%20id", "", true)
	if r.status != http.StatusBadRequest || errorCode(r) != "ids.invalid" {
		t.Fatalf("магазин не по формату: %+v", r)
	}

	if rows := e.store.OutboxRows(); len(rows) == 0 {
		t.Fatal("изменения участника ставят задачи синхронизации индекса")
	}
}

func TestCatalog(t *testing.T) {
	e := newEnv(t)
	e.seedTenant("t1", "shop-one")
	e.store.SeedParticipant(participant.State{
		TenantID:    "t1",
		Slug:        "shop-one",
		Name:        "Магазин",
		Status:      participant.StatusOnboarding,
		Enabled:     true,
		ShopEnabled: true,
		Version:     1,
		CreatedAt:   t0,
		UpdatedAt:   t0,
	})
	e.store.SeedTag(catalog.TagState{TenantID: "t1", TagID: "tag-1", Revision: 1, Value: "Новинка", Version: 1})
	e.seedOffer(t, "t1", "p1", 150000, "tag-1")
	e.seedOffer(t, "t1", "p2", 50000)

	r := e.call(t, http.MethodGet, "/api/v1/catalog/offers?sort=price_asc&limit=1", "", false)
	items, _ := r.body["items"].([]any)
	if r.status != http.StatusOK || len(items) != 1 || r.body["total"] != float64(2) || r.body["limit"] != float64(1) {
		t.Fatalf("выдача: %+v", r)
	}

	first, _ := items[0].(map[string]any)
	seller, _ := first["seller"].(map[string]any)
	if first["id"] != "t1~p2" || first["price"] != float64(50000) || seller["slug"] != "shop-one" ||
		first["checkoutAvailable"] != false {
		t.Fatalf("предложение в выдаче: %+v", first)
	}
	if _, ok := first["description"]; ok {
		t.Fatal("описание — только в карточке")
	}

	invalid := []string{
		"limit=101",
		"limit=x",
		"priceMin=1.5",
		"inStock=maybe",
		"sort=random",
		"priceMin=10&priceMax=1",
	}

	for _, query := range invalid {
		r := e.call(t, http.MethodGet, "/api/v1/catalog/offers?"+query, "", false)
		if r.status != http.StatusBadRequest || errorCode(r) != "catalog.invalid_query" {
			t.Fatalf("%s: %+v", query, r)
		}
	}

	r = e.call(t, http.MethodGet, "/api/v1/catalog/offers/t1~p1", "", false)
	tags, _ := r.body["tags"].([]any)
	tag, _ := tags[0].(map[string]any)
	if r.status != http.StatusOK || r.body["description"] != "Описание" || tag["label"] != "Новинка" {
		t.Fatalf("карточка: %+v", r)
	}

	r = e.call(t, http.MethodGet, "/api/v1/catalog/offers/t1~missing", "", false)
	if r.status != http.StatusNotFound || errorCode(r) != "catalog.offer_not_found" {
		t.Fatalf("нет предложения: %+v", r)
	}

	r = e.call(t, http.MethodGet, "/api/v1/catalog/offers/no-separator", "", false)
	if r.status != http.StatusBadRequest || errorCode(r) != "ids.invalid" {
		t.Fatalf("идентификатор предложения: %+v", r)
	}
}

func TestServiceRoutes(t *testing.T) {
	e := newEnv(t)

	r := e.call(t, http.MethodGet, "/nowhere", "", false)
	if r.status != http.StatusNotFound || errorCode(r) != "http.not_found" || r.header.Get("X-Request-Id") != "req-1" {
		t.Fatalf("неизвестный адрес: %+v", r)
	}

	if r := e.call(t, http.MethodGet, "/health", "", false); r.status != http.StatusOK {
		t.Fatal(r)
	}

	raw, err := http.Get(e.server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Body.Close() }()

	data, _ := io.ReadAll(raw.Body)
	if !strings.Contains(string(data), `marketplace_http_requests_total{code="404",route="/"}`) {
		t.Fatalf("метрики запросов по маршруту:\n%s", data)
	}
}

func TestUnexpectedErrorIsNotDisclosed(t *testing.T) {
	e := newEnv(t)
	e.store.FailCommit = errors.New("сбой фиксации")

	r := e.call(t, http.MethodGet, "/api/v1/operator/participants", "", true)
	if r.status != http.StatusInternalServerError || errorCode(r) != "internal" {
		t.Fatalf("непредвиденная ошибка не раскрывается: %+v", r)
	}
}
