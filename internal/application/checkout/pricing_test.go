package checkout_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestVerifiedBuyerReachesEverySellerOperation(t *testing.T) {
	e := setup(t)
	purchase := confirmedPurchase(t, e)
	contact := orderContact
	contact.Phone = "70000000000"

	if _, err := e.service.CreateOrders(context.Background(), buyerID, purchase.ID, contact); err != nil {
		t.Fatal(err)
	}
	calls := e.platform.ItemCheckCalls()
	if len(calls) == 0 {
		t.Fatal("не проверены цены магазина")
	}
	for _, call := range calls {
		if call.Request.BuyerID != buyerID {
			t.Fatalf("ценовой аккаунт потерян при проверке: %+v", call)
		}
	}
	quotes := e.platform.QuoteRequests()
	if len(quotes) == 0 {
		t.Fatal("не проверена доставка")
	}
	for _, request := range quotes {
		if request.BuyerID != buyerID {
			t.Fatalf("доставка посчитана для другого аккаунта: %+v", request)
		}
	}
	for _, tenant := range []ids.TenantID{"t1", "t2"} {
		requests := e.platform.OrderRequests(tenant)
		if len(requests) != 1 || requests[0].BuyerID != buyerID || requests[0].Contact.Phone != contact.Phone {
			t.Fatalf("аккаунт не зависит от контакта получателя: %+v", requests)
		}
	}
}
