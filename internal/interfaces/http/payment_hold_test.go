package http

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestCheckoutHTTPReadsHeldGroupAndPaysAvailableRemainder(t *testing.T) {
	e := newPaymentTest(t, true)
	ctx := context.Background()
	order, err := e.platform.CreateOrder(
		ctx,
		"t2",
		ports.CreateOrderRequest{
			ExternalCheckoutID: string(paymentTestID),
			GroupID:            "t2",
			Items:              []ports.OrderItem{{ProductID: "p1", Quantity: 1, Price: money.FromKopecks(500)}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	err = e.store.Do(
		ctx,
		func(ctx context.Context, tx ports.Tx) error {
			c, err := tx.Checkouts().Get(ctx, paymentTestID)
			if err != nil {
				return err
			}
			state := c.State()
			second := state.Groups[0]
			second.TenantID, second.OrderID = "t2", order.OrderID
			second.Lines = []checkout.Line{
				{
					Offer: ids.OfferID{
						Tenant:  "t2",
						Product: "p1",
					},
					Quantity: 1,
					Price:    money.FromKopecks(500),
				},
			}
			second.Goods = money.FromKopecks(500)
			state.Groups = append(state.Groups, second)
			state.Total, state.GoodsTotal = money.FromKopecks(12845), money.FromKopecks(12845)
			return tx.Checkouts().Update(ctx, checkout.Restore(state))
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	e.store.SeedVerdict(moderation.VerdictState{TenantID: "t1", ProductID: "p1", Status: moderation.Pending})
	path := "/api/v1/checkout/" + string(paymentTestID)
	response := e.call(http.MethodGet, path, e.cookie, "")
	var state checkoutJSON
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 ||
		state.PayableTotal != rubles(500) ||
		state.Groups[0].PaymentHold != checkout.HoldModerationPending {
		t.Fatal(response.Code, response.Body.String())
	}
	paid := e.call(
		http.MethodPost,
		"/api/v1/payment-stub/"+string(paymentTestID)+"/confirm",
		e.cookie,
		"https://market.example",
	)
	if paid.Code != http.StatusSeeOther {
		t.Fatal(paid.Code, paid.Body.String())
	}
	response = e.call(http.MethodGet, path, e.cookie, "")
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 ||
		state.Status != "partially_paid" ||
		state.PayableTotal != 0 ||
		state.PaidTotal != rubles(500) {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, endpoint := range []string{path + "/pay", "/api/v1/payment-stub/" + string(paymentTestID) + "/confirm"} {
		refused := e.call(http.MethodPost, endpoint, e.cookie, "https://market.example")
		var body errorBody
		if err := json.Unmarshal(refused.Body.Bytes(), &body); err != nil {
			t.Fatal(err, refused.Body.String())
		}
		if refused.Code != 409 ||
			body.Checkout == nil ||
			body.Checkout.Groups[0].PaymentHold == "" ||
			body.Checkout.PaidTotal != rubles(500) {
			t.Fatal(refused.Code, refused.Body.String())
		}
	}
	if len(e.platform.StubPaymentCalls()) != 1 || e.platform.StubPaymentCalls()[0] != "t2" {
		t.Fatal(e.platform.StubPaymentCalls())
	}
}
