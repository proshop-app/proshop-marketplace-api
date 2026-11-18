package checkout_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type lostCreateReply struct {
	ports.PlatformAPI
	calls   atomic.Int32
	created checkout.SellerOrder
}

func logPurchaseState(t *testing.T, label string, state checkout.State, err error) {
	t.Helper()
	amount, amountErr := appcheckout.PaymentAmount(state)
	t.Logf(
		"%s: err=%v status=%s total=%d payable=%d(%v)",
		label,
		err,
		state.Status,
		state.Total.Kopecks(),
		amount.Kopecks(),
		amountErr,
	)
	for _, g := range state.Groups {
		quote := "<nil>"
		if g.Quote != nil {
			quote = string(g.Quote.Status) + "/" + g.Quote.Reason
		}
		t.Logf(
			"   %s status=%s quote=%s excluded=%v exhausted=%v reconcile=%v order=%q",
			g.TenantID,
			g.Status,
			quote,
			g.Excluded(),
			g.CreateExhausted,
			g.ReconciliationRequired,
			g.OrderID,
		)
	}
}

// Готовность оплаченной группы не влияет на оставшийся платёж.
func TestPaidGroupDoesNotBlockRemainingPayment(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	state, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	e.platform.SetOrderStatus(state.Groups[0].OrderID, checkout.GroupPaid, "")
	e.clock.Advance(time.Minute)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	state, _ = e.service.Get(ctx, buyerID, purchase.ID)
	logPurchaseState(t, "после оплаты t1 у магазина", state, nil)
	product, _ := e.store.Product("t1", "p1")
	product.Card.Publication.Published = false
	e.store.SeedProduct(product)
	paid, payErr := e.service.Pay(ctx, buyerID, purchase.ID)
	logPurchaseState(t, "Pay", paid, payErr)
	confirmErr := e.service.ConfirmPayment(ctx, buyerID, purchase.ID)
	t.Logf("ConfirmPayment=%v; оплат: %v", confirmErr, e.platform.StubPaymentCalls())
	if payErr != nil || confirmErr != nil {
		t.Fatalf("готовность уже оплаченной группы блокирует оплату оставшихся")
	}
	if calls := e.platform.StubPaymentCalls(); len(calls) != 1 || calls[0] != "t2" {
		t.Fatal("повторно оплачена уже оплаченная группа", calls)
	}
}

func (p *lostCreateReply) CreateOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	if tenant != "t1" {
		return p.PlatformAPI.CreateOrder(ctx, tenant, request)
	}
	if p.calls.Add(1) == 1 {
		p.created, _ = p.PlatformAPI.CreateOrder(ctx, tenant, request)
	}
	return checkout.SellerOrder{}, context.DeadlineExceeded
}

// Истечение окна снимает резерв возможного заказа, сохраняя состав до решения покупателя.
func TestCreationExpiryReconcilesPossibleOrderWithoutDroppingGoods(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	upstream := &lostCreateReply{PlatformAPI: e.platform}
	svc := appcheckout.NewService(
		e.store,
		upstream,
		e.pickups,
		e.verifier,
		&fake.IDs{},
		e.clock,
		appcheckout.Config{
			QuoteTTL:      30 * time.Minute,
			QuoteTimeout:  time.Second,
			PaymentStub:   true,
			ReturnBaseURL: "https://market.example",
		},
	)
	state, err := svc.Create(ctx, buyerID, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Quote(ctx, buyerID, state.ID, "cdek", "MSK1", "Город"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Confirm(ctx, buyerID, state.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateOrders(ctx, buyerID, state.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if err := runTasks(ctx, e, svc); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Second)
	}
	for range 3 {
		if err := svc.Resume(ctx); err != nil {
			t.Fatal(err)
		}
		if err := runTasks(ctx, e, svc); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(10 * time.Minute)
	}
	after, _ := svc.Get(ctx, buyerID, state.ID)
	logPurchaseState(t, "после окна создания и фоновых проходов", after, nil)
	if after.Groups[0].Excluded() || after.Total != state.Total || !after.Groups[0].CancellationConfirmed {
		t.Fatal("сверка не должна менять состав", after)
	}
	t.Logf(
		"заказов у магазинов: %d; cancel-by-checkout t1: %d",
		e.platform.OrderCount(),
		len(e.platform.CancellationRequests("t1")),
	)
	if len(e.platform.CancellationRequests("t1")) == 0 {
		t.Fatalf("возможный заказ t1 (потерянный ответ) не сверяется и держит резерв до таймаута оплаты магазина")
	}
}

func TestReadingCashierDoesNotChangePurchaseOrScheduleCancellation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	before, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	product, _ := e.store.Product("t1", "p1")
	product.Card.Publication.Published = false
	e.store.SeedProduct(product)
	if _, err := e.service.Payment(ctx, buyerID, purchase.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := e.service.Get(ctx, buyerID, purchase.ID)
	if after.Version != before.Version || after.Status != before.Status || after.Total != before.Total {
		t.Fatal("чтение кассы изменило покупку", before, after)
	}
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	if len(e.platform.CancellationRequests("t1")) != 0 {
		t.Fatal("GET поставил отмену")
	}
	if _, err := e.service.Pay(ctx, buyerID, purchase.ID); !errors.Is(err, checkout.ErrPaymentPaused) {
		t.Fatal("POST должен приостановить оплату", err)
	}
}

func TestCreationExpiryKeepsOrderThatShopAlreadyPaid(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	upstream := &lostCreateReply{PlatformAPI: e.platform}
	service := appcheckout.NewService(
		e.store,
		upstream,
		e.pickups,
		e.verifier,
		&fake.IDs{},
		e.clock,
		appcheckout.Config{QuoteTTL: 30 * time.Minute, QuoteTimeout: time.Second, PaymentStub: true},
	)
	state, err := service.Create(ctx, buyerID, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Quote(ctx, buyerID, state.ID, "cdek", "MSK1", "Город"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Confirm(ctx, buyerID, state.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateOrders(ctx, buyerID, state.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	if err := runTasks(ctx, e, service); err != nil {
		t.Fatal(err)
	}
	e.platform.SetOrderStatus(upstream.created.OrderID, checkout.GroupPaid, "")
	e.clock.Advance(appcheckout.CreationTimeout + time.Second)
	for range 3 {
		if err := runTasks(ctx, e, service); err != nil {
			t.Fatal(err)
		}
	}
	current, err := service.Get(ctx, buyerID, state.ID)
	if err != nil || current.Status != checkout.StatusPartiallyPaid || !current.Groups[0].Paid() ||
		current.Groups[0].Excluded() {
		t.Fatal(current, err)
	}
	if _, err := service.RemoveGroup(ctx, buyerID, state.ID, "t1"); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal("оплаченную группу нельзя убрать", err)
	}
}

// Снятие неоплаченной группы после частичной оплаты сохраняет уже оплаченный заказ.
func TestReviewAfterPartialPaymentPreservesPaidOrder(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	state, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	paidOrder := state.Groups[0].OrderID
	e.platform.SetOrderStatus(paidOrder, checkout.GroupPaid, "")
	e.clock.Advance(time.Minute)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	seller, _ := e.store.Participant("t2")
	seller.Stage = participant.StageDisconnected
	e.store.SeedParticipant(seller)
	state, err = e.service.Pay(ctx, buyerID, purchase.ID)
	if !errors.Is(err, checkout.ErrDeliveryChanged) || state.Status != checkout.StatusDraft {
		t.Fatal(state, err)
	}
	if !state.Groups[0].Paid() || state.Groups[0].Excluded() || state.Groups[0].OrderID != paidOrder {
		t.Fatal("оплаченная группа снята проверкой готовности", state.Groups[0])
	}
	if !state.Groups[1].Excluded() {
		t.Fatal("неоплаченная неготовая группа не пересмотрена", state.Groups[1])
	}
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	state = reconfirmPurchase(t, e, purchase.ID)
	if state.Status != checkout.StatusPartiallyPaid || state.Groups[0].OrderID != paidOrder {
		t.Fatal(state)
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, purchase.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.platform.StubPaymentCalls()) != 0 || len(e.platform.CancellationRequests("t1")) != 0 ||
		len(e.platform.OrderRequests("t1")) != 1 {
		t.Fatal("оплаченный заказ повторён или отменён")
	}
}
