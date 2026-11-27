package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestCartPostRequiresAndReplaysKey(t *testing.T) {
	now := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	store, platform := fake.NewStore(), fake.NewPlatform()
	offer := ids.OfferID{Tenant: "t1", Product: "p1"}
	store.SeedBuyerOffer(offer, "Товар", money.FromKopecks(100))
	platform.SetItem(offer.Tenant, ports.ItemCheck{
		ProductID: offer.Product,
		Exists:    true,
		Available: 5,
		Price:     money.FromKopecks(100),
	})
	clock := fake.NewClock(now)
	h := cartHandlers{carts: appcart.NewService(store, platform, &fake.IDs{}, clock, time.Hour, "test-cart-key")}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/cart/add-key", nil)
	request = request.WithContext(context.WithValue(request.Context(), keyBuyer, ids.UUID("buyer")))
	issued := httptest.NewRecorder()
	h.newAddKey(issued, request)
	var token addKeyJSON
	if err := json.Unmarshal(issued.Body.Bytes(), &token); err != nil || issued.Code != http.StatusOK ||
		issued.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(issued.Code, issued.Body.String(), err)
	}
	for i, expected := range []int{http.StatusBadRequest, http.StatusOK, http.StatusOK, http.StatusConflict} {
		quantity := 1
		if i == 3 {
			quantity = 2
		}
		body := fmt.Sprintf(`{"offerId":"t1~p1","quantity":%d}`, quantity)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/cart/lines", strings.NewReader(body))
		if i != 0 {
			req.Header.Set("Idempotency-Key", token.Key)
			req.Header.Set("Date", now.Add(2*time.Minute).Format(http.TimeFormat))
		}
		req = req.WithContext(context.WithValue(req.Context(), keyBuyer, ids.UUID("buyer")))
		resp := httptest.NewRecorder()
		h.add(resp, req)
		if resp.Code != expected {
			t.Fatal(resp.Code, resp.Body.String())
		}
	}
	if state, ok := store.Cart("buyer"); !ok || len(state.Lines) != 1 || state.Lines[0].Quantity != 1 {
		t.Fatal(state)
	}
	if calls := platform.ItemCheckCalls(); len(calls) != 1 {
		t.Fatal(calls)
	}
}
