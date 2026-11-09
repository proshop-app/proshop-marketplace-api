package http_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestBuyerFeatureAuthorizationAndProfile(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"profile", "favorites", "price-watches", "subscriptions/shops"} {
		result := e.session(t, http.MethodGet, "/api/v1/buyer/"+path, "", "")
		if result.status != http.StatusUnauthorized || result.header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("авторизация %s: %+v", path, result)
		}
	}
	cookie := e.login(t)
	result := e.session(t, http.MethodPatch, "/api/v1/buyer/profile", `{"name":"Имя"}`, cookie)
	if result.status != http.StatusOK || result.body["name"] != "Имя" || result.body["region"] != nil {
		t.Fatalf("профиль: %+v", result)
	}
	result = e.session(t, http.MethodPatch, "/api/v1/buyer/profile", `{"region":{"code":"bad"}}`, cookie)
	if result.status != http.StatusBadRequest {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodGet, "/api/v1/region", "", "")
	if result.status != http.StatusOK || result.body["source"] != "none" {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodGet, "/api/v1/buyer/favorites?limit=-1", "", cookie)
	if result.status != http.StatusBadRequest {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodGet, "/api/v1/buyer/favorites", "", cookie)
	if result.status != http.StatusOK || len(result.body["items"].([]any)) != 0 {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodPut, "/api/v1/buyer/price-watches/t1~p1", `{"targetPrice":-1}`, cookie)
	if result.status != http.StatusBadRequest {
		t.Fatal(result)
	}
}

func TestBuyerFeatureCollectionsAndShopHeader(t *testing.T) {
	e := newEnv(t)
	cookie := e.login(t)
	e.seedTenant("t1", "shop")
	e.store.SeedParticipant(participant.State{
		TenantID: "t1",
		Slug:     "shop",
		Stage:    participant.StageConnected,
		Delivery: w3ServerDelivery,
		Subsidy:  w3ServerSubsidy,
	})
	e.seedOffer(t, "t1", "p1", 100)
	for _, collection := range []string{"favorites", "price-watches"} {
		path := "/api/v1/buyer/" + collection + "/t1~p1"
		for i := 0; i < 2; i++ {
			result := e.session(t, http.MethodPut, path, `{}`, cookie)
			if result.status != http.StatusNoContent {
				t.Fatal(result)
			}
		}
	}
	result := e.session(t, http.MethodGet, "/api/v1/buyer/favorites", "", cookie)
	items := result.body["items"].([]any)
	if result.status != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["listed"] != true {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodPut, "/api/v1/buyer/price-watches/t1~p1", `{"targetPrice":100}`, cookie)
	if result.status != http.StatusNoContent {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodGet, "/api/v1/buyer/price-watches", "", cookie)
	watch := result.body["items"].([]any)[0].(map[string]any)
	if result.status != http.StatusOK || watch["dropped"] != false {
		t.Fatal(result)
	}
	e.seedOffer(t, "t1", "p1", 90)
	result = e.session(t, http.MethodGet, "/api/v1/buyer/profile", "", cookie)
	if result.status != http.StatusOK || result.body["alerts"].(map[string]any)["priceDrops"] != float64(1) {
		t.Fatal(result)
	}
	e.platform.Fail("t1", errors.New("контексты недоступны"))
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		result = e.session(t, method, "/api/v1/buyer/profile", `{}`, cookie)
		alerts, ok := result.body["alerts"].(map[string]any)
		value, exists := alerts["priceDrops"]
		if result.status != http.StatusOK || !ok || !exists || value != nil {
			t.Fatal(result)
		}
	}
	e.platform.Fail("t1", nil)
	e.store.SeedProduct(catalog.ProductState{TenantID: "t1", ProductID: "p1", Deleted: true})
	result = e.session(t, http.MethodGet, "/api/v1/buyer/favorites", "", cookie)
	favorite := result.body["items"].([]any)[0].(map[string]any)
	if favorite["listed"] != false || favorite["name"] != "Товар p1" || favorite["inStock"] != false ||
		favorite["checkoutAvailable"] != false || favorite["available"] != float64(0) {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodGet, "/api/v1/buyer/price-watches", "", cookie)
	watch = result.body["items"].([]any)[0].(map[string]any)
	removed := watch["offer"].(map[string]any)
	if removed["inStock"] != false || removed["checkoutAvailable"] != false || removed["available"] != float64(0) ||
		watch["currentPrice"] != nil || watch["dropped"] != false {
		t.Fatal(result)
	}
	err := e.store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.StoreCards().Save(ctx, ports.StoreRecord{
			PagePublic: true,
			Header:     ports.ShopHeader{TenantID: "t1", Slug: "shop"},
			Card:       ports.ShopCard{TenantID: "t1", Slug: "shop"},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	result = e.session(t, http.MethodPut, "/api/v1/buyer/subscriptions/shops/t1", "", cookie)
	if result.status != http.StatusNoContent {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodGet, "/api/v1/shops/shop", "", cookie)
	if result.status != http.StatusOK || result.body["subscribed"] != true ||
		result.body["subscribers"] != float64(1) ||
		result.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodGet, "/api/v1/shops/shop", "", "")
	if _, exists := result.body["subscribed"]; exists || result.body["subscribers"] != float64(1) {
		t.Fatal("публичная шапка персонализирована")
	}
	for _, collection := range []string{"favorites/t1~p1", "price-watches/t1~p1", "subscriptions/shops/t1"} {
		for i := 0; i < 2; i++ {
			result = e.session(t, http.MethodDelete, "/api/v1/buyer/"+collection, "", cookie)
			if result.status != http.StatusNoContent {
				t.Fatal(result)
			}
		}
	}
	result = e.session(t, http.MethodGet, "/api/v1/shops/shop", "", cookie)
	if result.status != http.StatusOK || result.body["subscribers"] != float64(0) ||
		result.body["subscribed"] != false {
		t.Fatal(result)
	}
}

func TestBuyerProfilePickupAndRegionHTTP(t *testing.T) {
	e := newEnv(t)
	cookie := e.login(t)
	point := delivery.PickupPoint{
		Carrier:    delivery.Carrier("cdek"),
		Code:       "point-1",
		Address:    "Адрес справочника",
		RegionCode: "RU-AD",
		Active:     true,
	}
	e.store.SeedPickupPoint(point)
	result := e.session(
		t,
		http.MethodPatch,
		"/api/v1/buyer/profile",
		`{"pickupPoint":{"provider":"cdek","code":"point-1","address":"Подмена","regionCode":"RU-AL"}}`,
		cookie,
	)
	if result.status != http.StatusOK {
		t.Fatal(result)
	}
	pickup := result.body["pickupPoint"].(map[string]any)
	if pickup["address"] != point.Address || pickup["regionCode"] != point.RegionCode {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodGet, "/api/v1/region", "", cookie)
	if result.status != http.StatusOK || result.body["source"] != "pickup" {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodPatch, "/api/v1/buyer/profile", `{"region":{"code":"RU-AL"}}`, cookie)
	if result.status != http.StatusOK {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodGet, "/api/v1/region", "", cookie)
	if result.status != http.StatusOK || result.body["source"] != "profile" || result.body["code"] != "RU-AL" {
		t.Fatal(result)
	}
	point.Active = false
	e.store.SeedPickupPoint(point)
	result = e.session(
		t,
		http.MethodPatch,
		"/api/v1/buyer/profile",
		`{"pickupPoint":{"provider":"cdek","code":"point-1"}}`,
		cookie,
	)
	if result.status != http.StatusNotFound {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodGet, "/api/v1/region", "", "invalid")
	if result.status != http.StatusOK || result.body["source"] != "none" {
		t.Fatal(result)
	}
}

func TestBuyerPublicShopCacheAndHiddenSubscriptions(t *testing.T) {
	e := newEnv(t)
	cookie := e.login(t)
	record := ports.StoreRecord{
		Public: false,
		Header: ports.ShopHeader{TenantID: "t1", Slug: "shop"},
		Card:   ports.ShopCard{TenantID: "t1", Slug: "shop"},
	}
	save := func() {
		t.Helper()
		if err := e.store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
			return tx.StoreCards().Save(ctx, record)
		}); err != nil {
			t.Fatal(err)
		}
	}
	save()
	result := e.session(t, http.MethodPut, "/api/v1/buyer/subscriptions/shops/t1", "", cookie)
	if result.status != http.StatusNotFound {
		t.Fatal(result)
	}
	record.PagePublic = true
	save()
	result = e.session(t, http.MethodPut, "/api/v1/buyer/subscriptions/shops/t1", "", cookie)
	if result.status != http.StatusNoContent {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodGet, "/api/v1/buyer/subscriptions/shops", "", cookie)
	if result.status != http.StatusOK || len(result.body["items"].([]any)) != 1 {
		t.Fatal("открытая страница с неготовой карточкой потеряна", result)
	}
	for _, token := range []string{"", "invalid"} {
		result = e.session(t, http.MethodGet, "/api/v1/shops/shop", "", token)
		if result.status != http.StatusOK || !strings.Contains(result.header.Get("Cache-Control"), "public") ||
			result.header.Get("ETag") == "" ||
			!strings.Contains(strings.Join(result.header.Values("Vary"), ","), "Cookie, Authorization") {
			t.Fatal(result)
		}
		etag := result.header.Get("ETag")
		cached := e.session(t, http.MethodGet, "/api/v1/shops/shop", "", token, "If-None-Match", etag)
		if cached.status != http.StatusNotModified ||
			!strings.Contains(strings.Join(cached.header.Values("Vary"), ","), "Cookie, Authorization") {
			t.Fatal(cached)
		}
	}
	result = e.session(t, http.MethodGet, "/api/v1/shops/shop", "", cookie)
	if !strings.Contains(strings.Join(result.header.Values("Vary"), ","), "Cookie, Authorization") ||
		result.body["subscribed"] != true {
		t.Fatal(result)
	}
	e.clock.Advance(6 * time.Minute)
	result = e.session(t, http.MethodGet, "/api/v1/shops/shop", "", cookie)
	if result.status != http.StatusOK || result.body["subscribed"] != nil ||
		!strings.Contains(result.header.Get("Cache-Control"), "public") {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodGet, "/api/v1/region", "", cookie)
	if result.status != http.StatusOK || result.body["source"] != "none" {
		t.Fatal(result)
	}
	e.clock.Advance(-6 * time.Minute)
	record.PagePublic = false
	save()
	result = e.session(t, http.MethodGet, "/api/v1/buyer/subscriptions/shops", "", cookie)
	if result.status != http.StatusOK || len(result.body["items"].([]any)) != 0 {
		t.Fatal(result)
	}
}

func TestBuyerRegionAnonymousLimit(t *testing.T) {
	e := newEnv(t)
	e.limiter.RetryAfter = 17 * time.Second
	for i := 0; i < 30; i++ {
		result := e.session(t, http.MethodGet, "/api/v1/region", "", "")
		if result.status != http.StatusOK {
			t.Fatal(result)
		}
	}
	result := e.session(t, http.MethodGet, "/api/v1/region", "", "")
	if result.body["error"].(map[string]any)["message"] != "превышен лимит подсказок региона, повторите через 17s" {
		t.Fatal(result)
	}
	if result.status != http.StatusTooManyRequests || result.header.Get("Retry-After") != "17" {
		t.Fatal(result)
	}
}

func TestBuyerFavoritesPaginationHTTP(t *testing.T) {
	e := newEnv(t)
	cookie := e.login(t)
	e.seedTenant("t1", "shop")
	e.store.SeedParticipant(participant.State{
		TenantID: "t1",
		Slug:     "shop",
		Enabled:  true,
		Stage:    participant.StageConnected,
		Delivery: w3ServerDelivery,
		Subsidy:  w3ServerSubsidy,
	})
	for _, id := range []string{"p1", "p2", "p3"} {
		e.seedOffer(t, "t1", ids.ProductID(id), 100)
		result := e.session(t, http.MethodPut, "/api/v1/buyer/favorites/t1~"+id, "", cookie)
		if result.status != http.StatusNoContent {
			t.Fatal(result)
		}
	}
	result := e.session(t, http.MethodGet, "/api/v1/buyer/favorites?limit=1&offset=1", "", cookie)
	if result.status != http.StatusOK || result.body["total"] != float64(3) {
		t.Fatal(result)
	}
	items := result.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "t1~p2" {
		t.Fatal(result)
	}
	result = e.session(t, http.MethodGet, "/api/v1/buyer/favorites?limit=1&offset=20", "", cookie)
	if result.status != http.StatusOK || result.body["total"] != float64(3) || len(result.body["items"].([]any)) != 0 {
		t.Fatal(result)
	}
}

func TestBuyerRegionIPAndAuthenticatedLimit(t *testing.T) {
	e := newEnv(t)
	e.geo.Region = &buyer.Region{Code: "RU-AD"}
	cookie := e.login(t)
	for i := 0; i < 30; i++ {
		result := e.session(t, http.MethodGet, "/api/v1/region", "", "")
		if result.status != http.StatusOK || result.body["source"] != "ip" {
			t.Fatal(result)
		}
	}

	for i := 0; i < 31; i++ {
		result := e.session(t, http.MethodGet, "/api/v1/region", "", cookie)
		if result.status != http.StatusOK || result.body["source"] != "ip" {
			t.Fatal(result)
		}
	}
	result := e.session(t, http.MethodGet, "/api/v1/region", "", "")
	if result.status != http.StatusTooManyRequests {
		t.Fatal(result)
	}

}

func TestBuyerProfilePartialAndNull(t *testing.T) {
	e := newEnv(t)
	cookie := e.login(t)
	e.store.SeedPickupPoint(delivery.PickupPoint{
		Carrier:    "cdek",
		Code:       "p1",
		RegionCode: "RU-AD",
		Active:     true,
	})
	body := `{"region":{"code":"RU-AD"},"pickupPoint":{"provider":"cdek","code":"p1"},` +
		`"notifications":{"priceDrops":true,"shopNews":true}}`
	result := e.session(t, http.MethodPatch, "/api/v1/buyer/profile", body, cookie)
	if result.status != http.StatusOK {
		t.Fatal(result)
	}
	body = `{"region":null,"pickupPoint":null,"notifications":{"priceDrops":false}}`
	result = e.session(t, http.MethodPatch, "/api/v1/buyer/profile", body, cookie)
	if result.status != http.StatusOK || result.body["region"] != nil || result.body["pickupPoint"] != nil {
		t.Fatal(result)
	}
	notifications := result.body["notifications"].(map[string]any)
	if notifications["priceDrops"] != false || notifications["shopNews"] != true {
		t.Fatal(notifications)
	}
	for _, body := range []string{`{"unknown":true}`, `{"notifications":{"unknown":true}}`} {
		result = e.session(t, http.MethodPatch, "/api/v1/buyer/profile", body, cookie)
		if result.status != http.StatusBadRequest {
			t.Fatal(result)
		}
	}
}
