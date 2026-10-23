package http_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func seedPurchase(t *testing.T, e *env, owner, id ids.UUID) {
	t.Helper()
	c, err := checkout.New(
		id,
		owner,
		"order-test-key",
		[]checkout.Line{
			{
				Offer:    ids.OfferID{Tenant: "t1", Product: "p1"},
				Quantity: 1,
				Name:     "Товар",
				Price:    money.FromKopecks(10000),
			},
		},
		t0,
	)
	if err != nil {
		t.Fatal(err)
	}
	selection := checkout.Selection{
		Carrier:         "cdek",
		PickupPointCode: "P1",
		RegionCode:      "r1",
		City:            "Город",
		Address:         "Адрес",
	}
	if err := c.SelectDelivery(
		selection,
		map[ids.TenantID]delivery.Quote{
			"t1": {Status: delivery.QuoteAvailable, ExpiresAt: t0.Add(time.Hour)},
		},
		t0,
	); err != nil {
		t.Fatal(err)
	}
	e.platform.SetSettings("t1", ports.ShopSettings{
		Enabled:          true,
		FreeDeliveryFrom: money.Money{},
		Subsidy:          participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
		Delivery:         participant.DeliverySettings{Carriers: []string{"cdek"}, Regions: []string{"cdek:r1"}},
	})
	e.platform.SetQuote("t1", delivery.Offered{Available: true})
	if err := c.Confirm(t0); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, c)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBuyerOrdersAndReturnOnlyTrustPolling(t *testing.T) {
	e := newEnv(t)
	cookie := e.login(t)
	me := e.session(t, http.MethodGet, "/api/v1/me", "", cookie)
	owner := ids.UUID(me.body["id"].(string))
	id := ids.UUID("f0000000-0000-4000-8000-000000000001")
	seedPurchase(t, e, owner, id)
	e.platform.OrderNow = t0
	path := "/api/v1/checkout/" + string(id)
	body := `{"contact":{"phone":"79251234567","name":"Покупатель"}}`
	created := e.session(t, http.MethodPost, path+"/orders", body, cookie)
	if created.status != http.StatusOK || created.body["nextPayment"] == nil ||
		created.body["status"] != "awaiting_payment" {
		t.Fatalf("заказы: %+v", created)
	}
	paidURL := path + "/groups/t1/pay"
	link := e.session(t, http.MethodPost, paidURL, "", cookie)
	if link.status != http.StatusOK || link.body["url"] == "" {
		t.Fatalf("ссылка: %+v", link)
	}
	returned := e.session(t, http.MethodGet, path+"/return?status=paid&orderId=forged", "", cookie)
	state, _ := e.store.Checkout(id)
	if returned.status != http.StatusOK || returned.body["status"] != "checking_payment" ||
		state.Groups[0].Status != checkout.GroupAwaitingPayment {
		t.Fatalf("возврат не подтверждает оплату: %+v", returned)
	}
	e.clock.Advance(16 * time.Minute)
	operator := e.call(t, http.MethodGet, "/api/v1/operator/checkouts?stuck=true", "", true)
	items, _ := operator.body["items"].([]any)
	if operator.status != http.StatusOK || len(items) != 1 {
		t.Fatalf("панель: %+v", operator)
	}
	e.platform.SetOrderStatus(state.Groups[0].OrderID, checkout.GroupPaid, "", "")
	refreshed := e.session(t, http.MethodGet, path, "", cookie)
	if refreshed.status != http.StatusOK || refreshed.body["status"] != "paid" || refreshed.body["nextPayment"] != nil {
		t.Fatalf("источник статуса — магазин: %+v", refreshed)
	}
	cabinet := e.session(t, http.MethodGet, "/api/v1/orders", "", cookie)
	items, _ = cabinet.body["items"].([]any)
	if cabinet.status != http.StatusOK || len(items) != 1 {
		t.Fatalf("кабинет: %+v", cabinet)
	}
	if r := e.session(t, http.MethodPost, paidURL, "", cookie); r.status != http.StatusConflict {
		t.Fatalf("оплаченная группа не требует доплаты: %+v", r)
	}
}

func TestPurchaseEndpointsRequireOwner(t *testing.T) {
	e := newEnv(t)
	cookie := e.login(t)
	id := ids.UUID("f0000000-0000-4000-8000-000000000002")
	other := ids.UUID("f0000000-0000-4000-8000-000000000003")
	seedPurchase(t, e, other, id)
	path := "/api/v1/checkout/" + string(id)
	for _, request := range []struct{ method, suffix, body string }{
		{http.MethodGet, "", ""},
		{http.MethodGet, "/return?status=paid", ""},
		{http.MethodPost, "/orders", `{"contact":{"phone":"79251234567","name":"Покупатель"}}`},
		{http.MethodPost, "/groups/t1/pay", ""},
	} {
		r := e.session(t, request.method, path+request.suffix, request.body, cookie)
		if r.status != http.StatusNotFound {
			t.Fatalf("чужая покупка: %+v", r)
		}
		r = e.session(t, request.method, path+request.suffix, request.body, "")
		if r.status != http.StatusUnauthorized {
			t.Fatalf("нет сессии: %+v", r)
		}
	}
	if e.platform.OrderCount() != 0 {
		t.Fatal("чужой заказ не должен создаваться")
	}
	if r := e.call(t, http.MethodGet, "/api/v1/operator/checkouts", "", false); r.status != http.StatusUnauthorized {
		t.Fatalf("панель требует токен: %+v", r)
	}
}

func TestCatalogShowsDeliveryUnavailableOutsideSellerRegions(t *testing.T) {
	e := newEnv(t)
	e.seedTenant("t1", "s1")
	e.store.SeedParticipant(participant.State{
		TenantID:    "t1",
		Slug:        "s1",
		Name:        "Магазин",
		Enabled:     true,
		ShopEnabled: true,
		Version:     1,
		Delivery:    participant.DeliverySettings{Regions: []string{"cdek:r1"}, Carriers: []string{"cdek"}},
		Subsidy:     participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
	})
	e.seedOffer(t, "t1", "p1", 10000)
	for _, selected := range []struct {
		region  string
		allowed bool
	}{
		{"r1", true},
		{"other", false},
	} {
		path := "/api/v1/catalog/offers?deliveryCarrier=cdek&deliveryRegion=" + selected.region
		response := e.call(t, http.MethodGet, path, "", false)
		items, _ := response.body["items"].([]any)
		if response.status != http.StatusOK || len(items) != 1 {
			t.Fatalf("выдача: %+v", response)
		}
		item := items[0].(map[string]any)
		if item["deliveryAvailable"] != selected.allowed {
			t.Fatalf("регион: %+v", item)
		}
	}
}
