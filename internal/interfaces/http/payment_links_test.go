package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestCheckoutRepresentationExposesOnlyCurrentUnexpiredLink(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	state := checkout.State{Groups: []checkout.Group{
		{
			TenantID:     "t1",
			Status:       checkout.GroupAwaitingPayment,
			PaymentOrder: 1,
			PaymentURL:   "https://seller.example/one",
			ExpiresAt:    now.Add(time.Hour),
		},
		{
			TenantID:     "t2",
			Status:       checkout.GroupAwaitingPayment,
			PaymentOrder: 2,
			PaymentURL:   "https://seller.example/two",
			ExpiresAt:    now.Add(2 * time.Hour),
		},
	}}
	for _, tc := range []struct {
		at          time.Time
		firstStatus checkout.GroupStatus
		visible     ids.TenantID
	}{
		{now, checkout.GroupAwaitingPayment, "t1"},
		{now.Add(time.Hour), checkout.GroupAwaitingPayment, "t2"},
		{now, checkout.GroupPaid, "t2"},
		{now, checkout.GroupCancelled, "t2"},
		{now, checkout.GroupSellerUnreachable, "t2"},
		{now.Add(2 * time.Hour), checkout.GroupAwaitingPayment, ""},
	} {
		state.Groups[0].Status = tc.firstStatus
		next := checkout.Restore(state).NextPayment(tc.at)
		body, err := toCheckoutJSON(state, next)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Groups []map[string]any `json:"groups"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		for i, group := range state.Groups {
			value, exists := decoded.Groups[i]["paymentUrl"]
			if !exists {
				t.Fatal("недоступная ссылка должна быть null, а не отсутствовать")
			}
			if group.TenantID == tc.visible {
				if value != group.PaymentURL {
					t.Fatalf("текущая ссылка: %s", raw)
				}
			} else if value != nil {
				t.Fatalf("утечка ссылки из покупки/кабинета/панели: %s", raw)
			}
		}
	}
}

func TestDeliveryChangedIncludesSnapshotAndPaymentOrderUses409(t *testing.T) {
	state := checkout.State{
		Status:        checkout.StatusDraft,
		GoodsTotal:    money.FromKopecks(100),
		DeliveryTotal: money.FromKopecks(50),
		Total:         money.FromKopecks(150),
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/checkout/id/orders", nil)
	(&checkoutHandlers{}).respond(response, request, http.StatusOK, state, checkout.ErrDeliveryChanged)
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Checkout checkoutJSON `json:"checkout"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusConflict || body.Error.Code != "DELIVERY_CHANGED" || body.Checkout.Total != 150 {
		t.Fatalf("изменённая доставка: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	writeError(response, request, checkout.ErrPaymentOrder)
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusConflict || body.Error.Code != "PAYMENT_ORDER" {
		t.Fatal(response.Body.String())
	}
}
