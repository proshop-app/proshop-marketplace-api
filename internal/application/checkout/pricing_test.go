package checkout_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestRecipientContactReachesOrder(t *testing.T) {
	e := setup(t)
	purchase := confirmedPurchase(t, e)
	contact := orderContact
	contact.Phone = "70000000000"

	if _, err := beginAndExecute(context.Background(), e, buyerID, purchase.ID, contact); err != nil {
		t.Fatal(err)
	}
	calls := e.platform.ItemCheckCalls()
	if len(calls) == 0 {
		t.Fatal("не проверены цены магазина")
	}
	quotes := e.platform.QuoteRequests()
	if len(quotes) == 0 {
		t.Fatal("не проверена доставка")
	}
	for _, tenant := range []ids.TenantID{"t1", "t2"} {
		requests := e.platform.OrderRequests(tenant)
		if len(requests) != 1 || requests[0].Contact.Phone != contact.Phone {
			t.Fatalf("контакт получателя не передан магазину: %+v", requests)
		}
	}
}
