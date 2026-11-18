package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestQuoteResponseHasOnePurchasePickupAndIndependentShopDurations(t *testing.T) {
	directory, platform, store := pickupHTTP(t)
	id := ids.UUID("00000000-0000-4000-8000-000000000001")
	owner := ids.UUID("00000000-0000-4000-8000-000000000002")
	clock := fake.NewClock(time.Now())
	purchase, err := checkout.New(
		id,
		owner,
		"single-point-http",
		[]checkout.Line{
			{Offer: ids.OfferID{Tenant: "t1", Product: "p1"}, Name: "Товар", Quantity: 1},
			{Offer: ids.OfferID{Tenant: "t2", Product: "p1"}, Name: "Товар", Quantity: 1},
		},
		clock.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := purchase.SetDeliveryOptions(
		map[ids.TenantID][]string{"t1": {"cdek"}, "t2": {"cdek"}},
		map[ids.TenantID][]string{"t1": {"RU-AD"}, "t2": {"RU-AD"}},
		clock.Now(),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, purchase)
	}); err != nil {
		t.Fatal(err)
	}
	platform.SetQuote("t1", delivery.Offered{Available: true, Cost: money.FromKopecks(1000), MinDays: 2, MaxDays: 3})
	platform.SetQuote("t2", delivery.Offered{Available: true, Cost: money.FromKopecks(2000), MinDays: 5, MaxDays: 8})
	handler := &checkoutHandlers{checkouts: appcheckout.NewService(
		store,
		platform,
		directory,
		nil,
		&fake.IDs{},
		clock,
		appcheckout.Config{QuoteTTL: time.Minute, QuoteTimeout: time.Second},
	)}
	for _, suffix := range []string{"", `,"tenantId":"t1"`, `,"tenantIds":["t1","t2"]`} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
			`{"carrier":"cdek","pickupPointCode":"p1","city":"Город"`+suffix+`}`,
		))
		request.SetPathValue("checkoutId", string(id))
		request = request.WithContext(context.WithValue(request.Context(), keyBuyer, owner))
		response := httptest.NewRecorder()
		handler.quote(response, request)
		if suffix != "" {
			if response.Code != http.StatusBadRequest {
				t.Fatal(response.Code, response.Body)
			}
			continue
		}
		var body struct {
			Delivery selectionJSON                `json:"delivery"`
			Groups   []map[string]json.RawMessage `json:"groups"`
			Carriers []string                     `json:"carriers"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 200 ||
			body.Delivery.PickupPointCode != "p1" || len(body.Groups) != 2 || len(body.Carriers) != 1 {
			t.Fatal(response.Code, response.Body, err)
		}
		for _, group := range body.Groups {
			if _, found := group["delivery"]; found {
				t.Fatal("у группы не должно быть собственного выбора", group)
			}
			if string(group["deliveryPrice"]) != "0.00" {
				t.Fatal(group)
			}
		}
	}
	if len(platform.QuoteRequests()) != 2 {
		t.Fatal(platform.QuoteRequests())
	}
}
