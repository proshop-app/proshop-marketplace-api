package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestCartPromoHTTP(t *testing.T) {
	store, platform := fake.NewStore(), fake.NewPlatform()
	now := time.Now()
	buyer := ids.UUID("buyer")
	store.SeedCart(cart.State{
		ID:        "cart",
		BuyerID:   buyer,
		Version:   1,
		ExpiresAt: now.Add(time.Hour),
		Lines: []cart.Line{{
			Offer:    ids.OfferID{Tenant: "t1", Product: "p1"},
			Quantity: 2,
			Price:    money.FromKopecks(100),
		}},
	})
	platform.SetItem("t1", ports.ItemCheck{ProductID: "p1", Exists: true, Available: 10, Price: money.FromKopecks(100)})
	handler := cartHandlers{carts: appcart.NewService(
		store,
		fake.NewIndex(),
		platform,
		&fake.IDs{},
		fake.NewClock(now),
		time.Hour,
	)}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/v1/cart/groups/t1/promo-code", strings.NewReader(`{"code":"BAD"}`))
		req.SetPathValue("tenantId", "t1")
		req = req.WithContext(context.WithValue(req.Context(), keyBuyer, buyer))
		response := httptest.NewRecorder()
		handler.promo(response, req)
		if response.Code != http.StatusOK {
			t.Fatal(response.Code, response.Body.String())
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		var groups []map[string]json.RawMessage
		if err := json.Unmarshal(raw["groups"], &groups); err != nil {
			t.Fatal(err)
		}
		if _, duplicated := groups[0]["lines"]; duplicated {
			t.Fatal("строки продублированы в группе")
		}
		var body cartJSON
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Groups) != 1 || body.Groups[0].Total != 200 {
			t.Fatal(body)
		}
		promo := body.Groups[0].PromoCode
		if method == http.MethodPut && (promo == nil || promo.Valid || promo.Message == nil) {
			t.Fatal(body)
		}
		if method == http.MethodDelete && promo != nil {
			t.Fatal(body)
		}
	}
}

func TestCheckoutTrackingJSON(t *testing.T) {
	for _, tracking := range []*checkout.Tracking{
		nil,
		{Carrier: "carrier", Number: nil, URL: "https://example.test/123"},
	} {
		state := checkout.State{Groups: []checkout.Group{{TenantID: "t1", Tracking: tracking}}}
		body, err := toCheckoutJSON(state, nil)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), `"track":`) {
			t.Fatal("дублирующий формат track")
		}
		if tracking == nil && !strings.Contains(string(encoded), `"tracking":null`) {
			t.Fatal(string(encoded))
		}
		if tracking != nil && !strings.Contains(string(encoded), `"number":null`) {
			t.Fatal(string(encoded))
		}
	}
}

func TestPromoValidationHTTP(t *testing.T) {
	handler := cartHandlers{carts: appcart.NewService(
		fake.NewStore(),
		fake.NewIndex(),
		fake.NewPlatform(),
		&fake.IDs{},
		fake.NewClock(time.Now()),
		time.Hour,
	)}
	for _, code := range []string{"", "   ", strings.Repeat("я", cart.MaxPromoLength+1)} {
		body, err := json.Marshal(promoCodeJSON{Code: code})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPut, "/api/v1/cart/groups/t1/promo-code", strings.NewReader(string(body)))
		req.SetPathValue("tenantId", "t1")
		response := httptest.NewRecorder()
		handler.promo(response, req)
		if response.Code != http.StatusBadRequest {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	writeError(response, httptest.NewRequest(http.MethodPost, "/", nil), checkout.ErrPromoInvalid)
	body := response.Body.String()
	if response.Code != http.StatusBadRequest || !strings.Contains(body, checkout.ErrPromoInvalid.Key()) {
		t.Fatal(response.Code, body)
	}
}

func TestGetCartVerifiesAllGroups(t *testing.T) {
	store, platform := fake.NewStore(), fake.NewPlatform()
	now := time.Now()
	buyer := ids.UUID("buyer")
	store.SeedCart(cart.State{
		ID:        "cart",
		BuyerID:   buyer,
		Version:   1,
		ExpiresAt: now.Add(time.Hour),
		Lines: []cart.Line{
			{Offer: ids.OfferID{Tenant: "t1", Product: "p1"}, Quantity: 1, Price: money.FromKopecks(100)},
			{Offer: ids.OfferID{Tenant: "t2", Product: "p1"}, Quantity: 1, Price: money.FromKopecks(100)},
		},
	})
	for _, tenant := range []ids.TenantID{"t1", "t2"} {
		platform.SetItem(tenant, ports.ItemCheck{
			ProductID: "p1",
			Exists:    true,
			Available: 10,
			Price:     money.FromKopecks(200),
		})
	}
	handler := cartHandlers{carts: appcart.NewService(
		store,
		fake.NewIndex(),
		platform,
		&fake.IDs{},
		fake.NewClock(now),
		time.Hour,
	)}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cart", nil)
	req = req.WithContext(context.WithValue(req.Context(), keyBuyer, buyer))
	response := httptest.NewRecorder()
	handler.get(response, req)
	var body cartJSON
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || body.Total != 400 || len(platform.ItemCheckCalls()) != 2 {
		t.Fatal(response.Code, body, platform.ItemCheckCalls())
	}
}
