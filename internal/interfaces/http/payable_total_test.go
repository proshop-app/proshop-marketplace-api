package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/order"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestPayableTotal(t *testing.T) {
	cases := []struct {
		name   string
		status checkout.Status
		group  checkout.GroupStatus
		want   int64
	}{
		{"full", checkout.StatusAwaitingPayment, checkout.GroupAwaitingPayment, 25090},
		{"partial", checkout.StatusAwaitingPayment, checkout.GroupUnconfirmed, 12445},
		{"creating", checkout.StatusCreatingOrders, checkout.GroupCreating, 0},
		{"cancelled", checkout.StatusCancelled, checkout.GroupCancelled, 0},
		{"paid", checkout.StatusPaid, checkout.GroupPaid, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := checkout.State{
				Status: tc.status,
				Total:  money.FromKopecks(25090),
				Groups: []checkout.Group{
					{
						TenantID:      "t1",
						Status:        checkout.GroupAwaitingPayment,
						OrderID:       "o1",
						Goods:         money.FromKopecks(12345),
						DeliveryPrice: money.FromKopecks(100),
					},
					{
						TenantID:      "t2",
						Status:        tc.group,
						OrderID:       "o2",
						Goods:         money.FromKopecks(12345),
						DeliveryPrice: money.FromKopecks(300),
					},
				},
			}
			for i := range state.Groups {
				state.Groups[i].Lines = []checkout.Line{{Quantity: 1, Price: money.FromKopecks(12345)}}
			}
			if tc.status == checkout.StatusCancelled || tc.status == checkout.StatusPaid {
				state.Groups[0].Status = tc.group
			}
			if tc.group == checkout.GroupCreating || tc.group == checkout.GroupUnconfirmed {
				state.Groups[1].OrderID = ""
			}
			for _, snapshot := range []checkout.State{state, order.Snapshot(state).State()} {
				view, err := toCheckoutJSON(snapshot)
				if err != nil || view.PayableTotal != rubles(tc.want) || view.Total != rubles(25090) {
					t.Fatal(view, err)
				}
				if tc.want > 0 {
					amount, err := appcheckout.PaymentAmount(snapshot)
					if err != nil || amount.Kopecks() != int64(view.PayableTotal) {
						t.Fatal(amount, err)
					}
				}
			}
		})
	}
	view, err := toCheckoutJSON(checkout.State{Status: checkout.StatusAwaitingPayment})
	if err != nil || view.PayableTotal != 0 {
		t.Fatal(view, err)
	}
	raw, err := json.Marshal(view)
	if err != nil || !strings.Contains(string(raw), `"payableTotal":0`) {
		t.Fatal(string(raw), err)
	}
}

func TestPayableTotalMatchesCashier(t *testing.T) {
	for _, partial := range []bool{false, true} {
		testPayableCashier(t, partial)
	}
}

func testPayableCashier(t *testing.T, partial bool) {
	t.Helper()
	e := newPaymentTest(t, true)
	state, err := e.service.Get(context.Background(), paymentTestBuyer, paymentTestID)
	if err != nil {
		t.Fatal(err)
	}
	if partial {
		state.Total = money.FromKopecks(12645)
		state.Groups = append(
			state.Groups,
			checkout.Group{
				TenantID: "t2",
				Status:   checkout.GroupRejected,
				Goods:    money.FromKopecks(200),
				Lines:    []checkout.Line{{Quantity: 1, Price: money.FromKopecks(200)}},
			},
		)
		if err := e.store.Do(
			context.Background(),
			func(ctx context.Context, tx ports.Tx) error {
				return tx.Checkouts().Update(ctx, checkout.Restore(state))
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	view, err := toCheckoutJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(view)
	if err != nil || !strings.Contains(string(raw), `"payableTotal":123.45`) {
		t.Fatal(string(raw), err)
	}
	page := e.call(http.MethodGet, "/api/v1/payment-stub/"+string(paymentTestID), e.cookie, "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "123,45") {
		t.Fatal(page.Code, page.Body.String())
	}
	if err := e.service.ConfirmPayment(context.Background(), paymentTestBuyer, paymentTestID); err != nil {
		t.Fatal(err)
	}
}
