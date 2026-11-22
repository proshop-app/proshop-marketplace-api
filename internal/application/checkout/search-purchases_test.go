package checkout_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/searchfeedback"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

func trackCheckoutSearchPurchases(e *env) *searchfeedback.BuyerKeys {
	keys := searchfeedback.NewBuyerKeys(strings.Repeat("k", 32))
	e.service = appcheckout.NewService(
		e.store,
		e.platform,
		e.pickups,
		e.verifier,
		&fake.IDs{},
		e.clock,
		appcheckout.Config{
			SearchBuyerKeys: keys,
			PaymentStub:     true,
			QuoteTTL:        30 * time.Minute,
			QuoteTimeout:    time.Second,
		},
	)
	return keys
}

func TestSearchPurchaseRecordsDirectAndPolledGroupsOnlyOnce(t *testing.T) {
	e := setup(t)
	keys := trackCheckoutSearchPurchases(e)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	first, second := state.Groups[0], state.Groups[1]
	e.clock.Advance(time.Minute)
	firstPaid := e.clock.Now()
	e.platform.Fail(second.TenantID, errors.New("нет ответа магазина"))
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrPaymentUnavailable) {
		t.Fatal(err)
	}
	assertSearchPurchases(t, e, state, keys.Key(buyerID), []time.Time{firstPaid})

	e.clock.Advance(time.Minute)
	secondPaid := e.clock.Now()
	e.platform.Fail(second.TenantID, nil)
	e.platform.SetOrderStatus(second.OrderID, checkout.GroupPaid, "")
	paid, err := refreshOrders(ctx, e, buyerID, c.ID)
	if err != nil || paid.Status != checkout.StatusPaid {
		t.Fatal(paid.Status, err)
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Hour)
	e.platform.SetOrderStatus(first.OrderID, checkout.GroupShipped, "")
	if _, err := refreshOrders(ctx, e, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	assertSearchPurchases(t, e, state, keys.Key(buyerID), []time.Time{firstPaid, secondPaid})
}

func TestSearchPurchasesRollbackWithPaidTransition(t *testing.T) {
	e := setup(t)
	keys := trackCheckoutSearchPurchases(e)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	e.platform.AfterStubPayment = func() { e.store.FailCommit = errors.New("откат записи") }
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err == nil {
		t.Fatal("фиксация оплаты должна была откатиться")
	}
	saved, _ := e.store.Checkout(c.ID)
	if checkout.Restore(saved).HasPaidGroups() {
		t.Fatal(saved.Status)
	}
	assertSearchPurchases(t, e, state, keys.Key(buyerID), nil)

	e.store.FailCommit = nil
	e.platform.AfterStubPayment = nil
	e.clock.Advance(time.Minute)
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	assertSearchPurchases(t, e, state, keys.Key(buyerID), []time.Time{e.clock.Now(), e.clock.Now()})
}

func TestSearchPurchasePollWinsRaceWithDirectPayment(t *testing.T) {
	e := setup(t)
	keys := trackCheckoutSearchPurchases(e)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	polledAt := e.clock.Now()
	e.platform.AfterStubPayment = func() {
		e.platform.AfterStubPayment = nil
		if _, err := refreshOrders(ctx, e, buyerID, c.ID); err != nil {
			t.Fatal(err)
		}
		assertSearchPurchases(t, e, state, keys.Key(buyerID), []time.Time{polledAt})
		e.clock.Advance(time.Minute)
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	assertSearchPurchases(t, e, state, keys.Key(buyerID), []time.Time{polledAt, e.clock.Now()})
}

func assertSearchPurchases(t *testing.T, e *env, original checkout.State, key string, times []time.Time) {
	t.Helper()
	rows := e.store.OutboxRows()
	rows = slices.DeleteFunc(rows, func(row fake.OutboxRow) bool { return row.Kind != searchfeedback.TaskPurchase })
	if len(rows) != len(times) {
		t.Fatalf("событий оплаты %d, ожидалось %d", len(rows), len(times))
	}
	for index, row := range rows {
		group := original.Groups[index]
		eventID := "marketplace-paid:" + string(original.ID) + ":" + string(group.TenantID) + ":" + group.OrderID
		var purchase ports.SearchPurchase
		if err := json.Unmarshal(row.Payload, &purchase); err != nil {
			t.Fatal(err)
		}
		if row.DedupKey != purchase.EventID || purchase.EventID != eventID || row.TenantID != group.TenantID ||
			purchase.BuyerKey != key || purchase.Version != 1 || !purchase.OrderCreatedAt.Equal(group.AwaitingSince) ||
			!purchase.PaidObservedAt.Equal(times[index]) || !row.CreatedAt.Equal(times[index]) ||
			!slices.Equal(purchase.OfferIDs, []string{group.Lines[0].Offer.String()}) {
			t.Fatal(string(row.Payload), row.TenantID, row.CreatedAt)
		}
	}
}
