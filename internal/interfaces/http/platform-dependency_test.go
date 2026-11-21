package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/platformguard"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apppromotion "github.com/proshop-app/proshop-marketplace/internal/application/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type dependencyFixture struct {
	store     *fake.Store
	upstream  *fake.Platform
	guarded   *platformguard.Client
	metrics   *fake.PlatformMetrics
	clock     *fake.Clock
	catalog   *catalogHandlers
	promotion *promotionHandlers
	cart      *cartHandlers
	buyer     *buyerFeatureHandlers
	shops     *participantHandlers
}

type dependencyRoute struct {
	path      string
	handler   func(*dependencyFixture) http.HandlerFunc
	operation string
	array     string
}

func newDependencyFixture(t *testing.T) *dependencyFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	clock := fake.NewClock(now)
	store, index, upstream := fake.NewStore(), fake.NewIndex(), fake.NewPlatform()
	store.SeedTaxonomy(taxonomy.Definition{
		Categories: []taxonomy.Category{{ID: "c1", Name: "Категория", Slug: "c1"}},
	})
	store.SeedTenant(participant.TenantState{TenantID: "t1", Slug: "shop", Status: participant.TenantActive})
	store.SeedParticipant(participant.State{
		TenantID: "t1",
		Slug:     "shop",
		Name:     "Магазин",
		Stage:    participant.StageConnected,
		Enabled:  true,
		Delivery: participant.DeliverySettings{Regions: []string{"RU-MOW"}, Carriers: []string{"cdek", "yandex"}},
	})
	store.SeedProduct(catalog.ProductState{
		TenantID:  "t1",
		ProductID: "p1",

		Available: 3,
		Card: catalog.Card{
			Name:        "Товар",
			Visible:     true,
			Publication: taxonomy.Publication{Published: true, CategoryID: "c1"},
		},
		HasPrice:  true,
		Price:     money.FromKopecks(10000),
		BasePrice: money.FromKopecks(10000),
	})
	store.SeedVerdict(moderation.VerdictState{TenantID: "t1", ProductID: "p1", Status: moderation.Allowed})
	var state ports.OfferState
	var doc ports.OfferDocument
	err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		state, err = tx.Offers().Offer(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		doc, _ = appcatalog.Document(state)
		for _, kind := range []ports.SavedKind{ports.Favorites, ports.PriceWatches} {
			if err := tx.BuyerFeatures().PutSaved(ctx, "b1", kind, ports.SavedOffer{
				Offer:        ports.Snapshot(doc),
				PriceAtWatch: money.FromKopecks(10000),
			}); err != nil {
				return err
			}
		}
		return tx.StoreCards().Save(ctx, ports.StoreRecord{
			Public:     true,
			PagePublic: true,
			Header:     ports.ShopHeader{TenantID: "t1", Slug: "shop", Name: "Магазин"},
			Card:       ports.ShopCard{TenantID: "t1", Slug: "shop", Name: "Магазин"},
		})
	})
	if err != nil || !doc.Visible {
		t.Fatal("фикстура не видна", err, doc)
	}
	doc.Version = 1
	if err := index.Put(ctx, []ports.OfferDocument{doc}); err != nil {
		t.Fatal(err)
	}
	id := ids.OfferID{Tenant: "t1", Product: "p1"}
	basket := cart.New("cart1", "b1", now, time.Hour)
	item := cart.Offer{
		ID:        id,
		Name:      "Товар",
		Price:     money.FromKopecks(10000),
		Visible:   true,
		Available: 3,
	}
	if err := basket.Add(item, 1, now, time.Hour); err != nil {
		t.Fatal(err)
	}
	store.SeedCart(basket.State())
	store.SeedPromotion(promotion.CampaignState{
		ID:            "ad1",
		TenantID:      "t1",
		PlacementCode: "category_slot",
		Status:        promotion.Active,
		Target:        promotion.Target{OfferID: id, CategoryID: "c1"},
		Period:        promotion.Period{From: "2026-11-01", To: "2026-11-02"},
	})
	upstream.Reviews = []ports.Review{{Author: "Имя", Rating: 5, CreatedAt: now}}
	metrics := &fake.PlatformMetrics{}
	guardConfig := platformguard.Config{
		ReadTimeout:           50 * time.Millisecond,
		StrictTimeout:         10 * time.Millisecond,
		CacheTTL:              time.Second,
		StaleTTL:              time.Minute,
		CacheSize:             16,
		TenantMaxConcurrent:   2,
		CheckoutMaxConcurrent: 2,
		ReadMaxConcurrent:     2,
		QueueSize:             8,
		TenantQueueSize:       2,
		TenantStateSize:       100,
		TenantIdleTTL:         time.Minute,
		MaxConcurrent:         4,
		FailureWindow:         30 * time.Second,
		FailureMinCalls:       20,
		FailurePercent:        50,
		FailureThreshold:      5,
		OpenInterval:          time.Second,
		LogInterval:           time.Minute,
	}
	guarded := platformguard.New(upstream, guardConfig, clock, metrics, nil)
	features := &fake.CatalogFeatures{States: []ports.OfferState{state}}
	queries := appcatalog.NewQueries(store, index, guarded, features, features, clock)
	promotions := apppromotion.NewService(store, clock, &fake.IDs{}, index, apppromotion.Config{
		Catalog:     queries,
		Location:    time.UTC,
		Random:      fake.Random{},
		SearchDepth: 100,
	})
	buyers := appbuyer.NewFeatures(store, nil, fake.NewLimiter(), &fake.PickupLookup{})
	return &dependencyFixture{
		store:     store,
		upstream:  upstream,
		guarded:   guarded,
		metrics:   metrics,
		clock:     clock,
		catalog:   &catalogHandlers{queries: queries},
		promotion: &promotionHandlers{service: promotions},
		cart:      &cartHandlers{carts: appcart.NewService(store, index, guarded, &fake.IDs{}, clock, time.Hour)},
		buyer:     &buyerFeatureHandlers{service: buyers},
		shops: &participantHandlers{
			service:       appparticipant.NewService(store, clock, &fake.IDs{}),
			buyerFeatures: buyers,
		},
	}
}

func dependencyRoutes() []dependencyRoute {
	return []dependencyRoute{
		{
			path: "/api/v1/catalog/offers",
			handler: func(f *dependencyFixture) http.HandlerFunc {
				return f.catalog.list
			},
			operation: "",
			array:     "items",
		},
		{
			path: "/api/v1/catalog/offers?tenant=t1&groupId=g1",
			handler: func(f *dependencyFixture) http.HandlerFunc {
				return f.catalog.list
			},
			operation: "",
			array:     "items",
		},
		{
			path: "/api/v1/recommendations?context=home",
			handler: func(f *dependencyFixture) http.HandlerFunc {
				return f.catalog.recommendations
			},
			operation: "",
			array:     "items",
		},
		{
			path: "/api/v1/recommendations?context=offer&offerId=t1~p1",
			handler: func(f *dependencyFixture) http.HandlerFunc {
				return f.catalog.recommendations
			},
			operation: "",
			array:     "items",
		},
		{
			path: "/api/v1/catalog/sponsored?category=c1",
			handler: func(f *dependencyFixture) http.HandlerFunc {
				return f.promotion.sponsored
			},
			operation: "",
			array:     "items",
		},
		{
			path: "/api/v1/catalog/sponsored?q=Товар",
			handler: func(f *dependencyFixture) http.HandlerFunc {
				return f.promotion.sponsored
			},
			operation: "",
			array:     "items",
		},
		{
			path: "/api/v1/catalog/offers/t1~p1",
			handler: func(f *dependencyFixture) http.HandlerFunc {
				return f.catalog.card
			},
			operation: "",
			array:     "",
		},
		{
			path: "/api/v1/catalog/offers/t1~p1/reviews",
			handler: func(f *dependencyFixture) http.HandlerFunc {
				return f.catalog.reviews
			},
			operation: "reviews",
			array:     "items",
		},
		{
			path: "/api/v1/cart",
			handler: func(f *dependencyFixture) http.HandlerFunc {
				return f.cart.get
			},
			operation: "",
			array:     "",
		},
		{
			path: "/api/v1/buyer/favorites",
			handler: func(f *dependencyFixture) http.HandlerFunc {
				return f.buyer.favorites
			},
			operation: "",
			array:     "",
		},
		{
			path: "/api/v1/buyer/price-watches",
			handler: func(f *dependencyFixture) http.HandlerFunc {
				return f.buyer.watches
			},
			operation: "",
			array:     "",
		},
		{
			path: "/api/v1/buyer/profile",
			handler: func(f *dependencyFixture) http.HandlerFunc {
				return f.buyer.profile
			},
			operation: "",
			array:     "",
		},
		{
			path: "/api/v1/shops",
			handler: func(f *dependencyFixture) http.HandlerFunc {
				return f.shops.shops
			},
			operation: "",
			array:     "",
		},
		{
			path: "/api/v1/shops/shop",
			handler: func(f *dependencyFixture) http.HandlerFunc {
				return f.shops.shop
			},
			operation: "",
			array:     "",
		},
	}
}

func TestPlatformDependencyReadRoutes(t *testing.T) {
	for _, route := range dependencyRoutes() {
		for _, stale := range []bool{false, true} {
			for _, timeout := range []bool{false, true} {
				name := route.path
				if stale {
					name += "/stale"
				}
				if timeout {
					name += "/timeout"
				}
				t.Run(name, func(t *testing.T) {
					f := newDependencyFixture(t)
					if stale {
						_, _ = f.guarded.ProductReviews(context.Background(), "t1", "p1", 20, 0)
						f.clock.Advance(2 * time.Second)
					}
					f.upstream.CallHook = func(ctx context.Context, _ string) error {
						if timeout {
							<-ctx.Done()
							return ctx.Err()
						}
						return errors.New("ячейка недоступна")
					}
					request := httptest.NewRequest(http.MethodGet, route.path, nil)
					request.SetPathValue("offerId", "t1~p1")
					request.SetPathValue("slug", "shop")
					request = request.WithContext(context.WithValue(request.Context(), keyBuyer, ids.UUID("b1")))
					response := httptest.NewRecorder()
					route.handler(f)(response, request)
					if response.Code != http.StatusOK {
						t.Fatal(response.Code, response.Body.String())
					}
					var body map[string]any
					if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if route.operation != "" {
						outcome := "error"
						if timeout {
							outcome = "timeout"
						}
						if f.metrics.Count(route.operation, outcome) != 1 {
							t.Fatal("маршрут не проверил отказ зависимости", route.path)
						}
						if stale && route.operation == "reviews" && f.metrics.Count(route.operation, "stale") != 1 {
							t.Fatal("маршрут не использовал stale", route.path)
						}
					}
					if !stale && route.operation == "reviews" && len(body[route.array].([]any)) != 0 {
						t.Fatal("блок не пустой", body)
					}
					if route.path == "/api/v1/cart" && len(body["lines"].([]any)) != 1 {
						t.Fatal("корзина потеряла сохранённый снимок", body)
					}
					if route.path == "/api/v1/catalog/offers/t1~p1" {
						price := float64(100)
						if body["price"] != price {
							t.Fatal("цена карточки", body)
						}
					}
					if stale && (route.path == "/api/v1/catalog/offers" || route.operation == "reviews") {
						if len(body["items"].([]any)) != 1 {
							t.Fatal("потеряна последняя успешная выдача", body)
						}
					}
				})
			}
		}
	}
}

func TestPlatformDependencyCartVerifyStaysStrict(t *testing.T) {
	f := newDependencyFixture(t)
	f.upstream.CallHook = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/cart/verify", nil)
	request = request.WithContext(context.WithValue(request.Context(), keyBuyer, ids.UUID("b1")))
	response := httptest.NewRecorder()
	f.cart.verify(response, request)
	var body errorBody
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusServiceUnavailable || body.Error.Code != "cart.seller_unreachable" {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestPublicPriceMatchesAuthenticatedResponse(t *testing.T) {
	f := newDependencyFixture(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/offers", nil)
	before := httptest.NewRecorder()
	f.catalog.list(before, request)
	request = request.WithContext(context.WithValue(request.Context(), keyBuyer, ids.UUID("b1")))
	after := httptest.NewRecorder()
	f.catalog.list(after, request)
	if before.Code != http.StatusOK || before.Body.String() != after.Body.String() {
		t.Fatal(before.Body.String(), after.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(after.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, found := body["pricing"]; found {
		t.Fatal("гостевой контракт получил новое поле", after.Body.String())
	}
}
