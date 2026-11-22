package checkout_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/searchfeedback"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

type searchPurchaseProbe struct {
	ports.UnitOfWork
	calls   atomic.Int64
	batches atomic.Int64
	fail    bool
}

type searchPurchaseProbeTx struct {
	ports.Tx
	probe *searchPurchaseProbe
}

type searchPurchaseProbeOutbox struct {
	ports.Outbox
	probe *searchPurchaseProbe
}

func (p *searchPurchaseProbe) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	p.calls.Add(1)
	return p.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return fn(ctx, searchPurchaseProbeTx{Tx: tx, probe: p})
	})
}

func (tx searchPurchaseProbeTx) Outbox() ports.Outbox {
	return searchPurchaseProbeOutbox{Outbox: tx.Tx.Outbox(), probe: tx.probe}
}

func (o searchPurchaseProbeOutbox) EnqueueBatch(ctx context.Context, tasks []ports.NewTask) error {
	o.probe.batches.Add(1)
	if o.probe.fail {
		return errors.New("outbox недоступен")
	}
	return o.Outbox.EnqueueBatch(ctx, tasks)
}

func trackCheckoutSearchPurchases(e *env) *searchfeedback.BuyerKeys {
	return trackCheckoutSearchPurchasesWithUOW(e, e.store)
}

func trackCheckoutSearchPurchasesWithUOW(e *env, uow ports.UnitOfWork) *searchfeedback.BuyerKeys {
	keys := searchfeedback.NewBuyerKeys(strings.Repeat("k", 32))
	e.service = appcheckout.NewService(
		uow,
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

func TestSearchPurchaseRecordsDirectAndRecoveryGETGroupsOnlyOnce(t *testing.T) {
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
	if err := announceOrder(ctx, e, first.TenantID, c.ID, first.OrderID); err != nil {
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

func TestSearchPurchaseEventWinsRaceWithDirectPayment(t *testing.T) {
	e := setup(t)
	keys := trackCheckoutSearchPurchases(e)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	observedAt := e.clock.Now()
	e.platform.AfterStubPayment = func() {
		e.platform.AfterStubPayment = nil
		first := state.Groups[0]
		if err := announceOrder(ctx, e, first.TenantID, c.ID, first.OrderID); err != nil {
			t.Fatal(err)
		}
		assertSearchPurchases(t, e, state, keys.Key(buyerID), []time.Time{observedAt})
		e.clock.Advance(time.Minute)
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	assertSearchPurchases(t, e, state, keys.Key(buyerID), []time.Time{observedAt, e.clock.Now()})
}

func TestSearchPurchaseObservedOnlyByShop(t *testing.T) {
	for _, channel := range []string{"event", "hold", "recovery_get"} {
		t.Run(channel, func(t *testing.T) {
			e := setup(t)
			keys := trackCheckoutSearchPurchases(e)
			c := confirmedPurchase(t, e)
			ctx := context.Background()
			state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
			if err != nil {
				t.Fatal(err)
			}
			first := state.Groups[0]

			e.platform.SetOrderStatus(first.OrderID, checkout.GroupPaid, "")
			e.clock.Advance(time.Minute)
			if channel == "recovery_get" {
				e.clock.Advance(checkout.PaymentWindow + checkout.PaymentExpiryGrace)
			}
			observedAt := e.clock.Now()
			for range 2 {
				switch channel {
				case "event":
					err = announceOrder(ctx, e, first.TenantID, c.ID, first.OrderID)
				case "hold":
					_, err = e.service.Pay(ctx, buyerID, c.ID)
				case "recovery_get":
					_, err = refreshOrders(ctx, e, buyerID, c.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				assertSearchPurchases(t, e, state, keys.Key(buyerID), []time.Time{observedAt})
				e.clock.Advance(time.Second)
			}
		})
	}
}

func TestSearchPurchaseEventCommitAndOptionalFailure(t *testing.T) {
	for _, failure := range []string{"commit", "outbox"} {
		t.Run(failure, func(t *testing.T) {
			e := setup(t)
			probe := &searchPurchaseProbe{UnitOfWork: e.store}
			keys := trackCheckoutSearchPurchasesWithUOW(e, probe)
			c := confirmedPurchase(t, e)
			ctx := context.Background()
			state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
			if err != nil {
				t.Fatal(err)
			}
			first := state.Groups[0]
			e.platform.SetOrderStatus(first.OrderID, checkout.GroupPaid, "")
			e.clock.Advance(time.Minute)
			if failure == "commit" {
				e.store.FailCommit = errors.New("откат покупки")
			} else {
				probe.fail = true
			}
			err = announceOrder(ctx, e, first.TenantID, c.ID, first.OrderID)
			if (err != nil) != (failure == "commit") {
				t.Fatal(err)
			}
			saved, _ := e.store.Checkout(c.ID)
			if saved.Groups[0].Paid() != (failure == "outbox") {
				t.Fatal(saved.Groups[0])
			}
			assertSearchPurchases(t, e, state, keys.Key(buyerID), nil)
			if failure == "commit" {
				if probe.batches.Load() != 0 {
					t.Fatal("сигнал поставлен до фиксации покупки")
				}
				e.store.FailCommit = nil
				if err := announceOrder(ctx, e, first.TenantID, c.ID, first.OrderID); err != nil {
					t.Fatal(err)
				}
				assertSearchPurchases(t, e, state, keys.Key(buyerID), []time.Time{e.clock.Now()})
			}
		})
	}
}

func TestSearchPurchaseEventWithoutPaidTransitionDoesNotOpenExtraTransaction(t *testing.T) {
	e := setup(t)
	probe := &searchPurchaseProbe{UnitOfWork: e.store}
	trackCheckoutSearchPurchasesWithUOW(e, probe)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	first := state.Groups[0]
	for range 2 {
		calls := probe.calls.Load()
		if err := announceOrder(ctx, e, first.TenantID, c.ID, first.OrderID); err != nil {
			t.Fatal(err)
		}
		if probe.calls.Load()-calls != 1 || probe.batches.Load() != 0 {
			t.Fatal("неоплаченный заказ и повтор события требуют только исходной транзакции")
		}
	}
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
