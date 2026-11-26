package checkout_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type cancellationEvents struct{ refused []ids.TenantID }

type delayedCancellation struct {
	ports.PlatformAPI
	started chan<- struct{}
	release <-chan struct{}
}

type cancellationAnswer struct {
	ports.PlatformAPI
	answer  ports.CancelOrderResult
	failure error
}

func (p cancellationAnswer) CancelOrder(
	context.Context,
	ids.TenantID,
	ports.CancelOrderRequest,
) (ports.CancelOrderResult, error) {
	return p.answer, p.failure
}

func (o *cancellationEvents) RecordCheckoutEvent(tenant ids.TenantID, _ ids.UUID, event string) {
	if event == "cancellation_refused" {
		o.refused = append(o.refused, tenant)
	}
}

func (p delayedCancellation) CancelOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CancelOrderRequest,
) (ports.CancelOrderResult, error) {
	if tenant == "t1" {
		p.started <- struct{}{}
		select {
		case <-p.release:
		case <-ctx.Done():
			return ports.CancelOrderResult{}, ctx.Err()
		}
	}
	return p.PlatformAPI.CancelOrder(ctx, tenant, request)
}

func TestBuyerCancelsLostAndLateOrdersAfterDeadline(t *testing.T) {
	for _, lost := range []bool{false, true} {
		e := setup(t)
		c := confirmedPurchase(t, e)
		ctx := context.Background()
		if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
			t.Fatal(err)
		}
		task := firstCreationTask(t, e)
		failure := errors.New("фиксация результата не удалась")
		e.platform.AfterCreate = func() {
			if lost {
				e.store.FailCommit = failure
				return
			}
			e.clock.Advance(appcheckout.CreationTimeout)
			for _, row := range e.store.OutboxRows() {
				if row.Kind == appcheckout.TaskExpireCreation && row.TenantID == task.TenantID {
					if err := e.service.HandleOrderTask(ctx, ports.Task{
						Kind:     row.Kind,
						TenantID: row.TenantID,
						Payload:  row.Payload,
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		err := e.service.HandleOrderTask(ctx, task)
		if lost && !errors.Is(err, failure) {
			t.Fatal(err)
		}
		if !lost && err != nil {
			t.Fatal(err)
		}
		e.store.FailCommit, e.platform.AfterCreate = nil, nil
		if lost {
			e.clock.Advance(appcheckout.CreationTimeout)
			if err := executeTasks(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		before, err := e.service.Get(ctx, buyerID, c.ID)
		if err != nil || (before.Groups[0].OrderID == "") != lost {
			t.Fatal(before, err)
		}
		if _, err := e.service.Cancel(ctx, buyerID, c.ID); err != nil {
			t.Fatal(err)
		}
		if err := executeTasks(ctx, e); err != nil {
			t.Fatal(err)
		}
		state, err := e.service.Get(ctx, buyerID, c.ID)
		g := state.Groups[0]
		if err != nil || g.ReconciliationRequired || !g.CancellationConfirmed || g.OrderID == "" ||
			g.Status != checkout.GroupCancelled {
			t.Fatal(state, err)
		}
		remote, err := e.platform.GetOrder(ctx, g.TenantID, g.OrderID)
		if err != nil || remote.Status != checkout.GroupCancelled {
			t.Fatal(remote, err)
		}
		requests := e.platform.CancellationRequests(g.TenantID)
		if len(requests) != 1 || requests[0].Reason != "buyer_cancelled" {
			t.Fatal(requests)
		}
	}
}

func TestCancellationAbsentBlocksLateCreation(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	e.platform.FailCreate("t1", &ports.OrderRefusal{Code: "MARKETPLACE_TEMPORARY_FAILURE"})
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	request := e.platform.OrderRequests("t1")[0]
	e.clock.Advance(appcheckout.CreationTimeout)
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Cancel(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].ReconciliationRequired || !state.Groups[0].CancellationConfirmed ||
		state.Groups[0].OrderID != "" {
		t.Fatal(state, err)
	}
	e.platform.FailCreate("t1", nil)
	_, err = e.platform.CreateOrder(ctx, "t1", request)
	var refusal *ports.OrderRefusal
	if !errors.As(err, &refusal) || refusal.Code != "ORDER_CHECKOUT_CANCELLED" {
		t.Fatal(err)
	}
	result, err := e.platform.CancelOrder(ctx, "t1", ports.CancelOrderRequest{
		ExternalCheckoutID: request.ExternalCheckoutID,
		GroupID:            "t1",
		Reason:             "buyer_cancelled",
	})
	if err != nil || !result.Absent {
		t.Fatal(result, err)
	}
}

func TestBuyerCancellationIsConfirmedOnce(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := e.service.Cancel(ctx, buyerID, c.ID); err != nil {
			t.Fatal(err)
		}
		if err := executeTasks(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range state.Groups {
		if g.Status != checkout.GroupCancelled || g.ReconciliationRequired || !g.CancellationConfirmed ||
			g.CreationResult() != checkout.CreationCreated {
			t.Fatal(g)
		}
		requests := e.platform.CancellationRequests(g.TenantID)
		if len(requests) != 1 || requests[0].Reason != "buyer_cancelled" {
			t.Fatal(requests)
		}
	}
}

func TestPaidOrderCancellationStaysVisibleToOperator(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	e.platform.SetOrderStatus(state.Groups[0].OrderID, checkout.GroupPaid, "")
	observer := &cancellationEvents{}
	e.service.SetObserver(observer)
	if _, err := e.service.Cancel(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	stuck, err := e.service.OperatorList(ctx, "", true)
	if err != nil || len(stuck) != 1 {
		t.Fatal(stuck, err)
	}
	g := stuck[0].Groups[0]
	if !g.ReconciliationRequired || !g.CancellationBlocked || g.CancellationConfirmed ||
		g.Reason != "order_not_cancellable:paid" || g.Status != checkout.GroupCancellationRequested {
		t.Fatal(g)
	}
	if len(observer.refused) != 1 || observer.refused[0] != "t1" {
		t.Fatal(observer)
	}
	if len(e.platform.CancellationRequests("t1")) != 1 {
		t.Fatal("409 не повторяется")
	}
}

func TestCancelledCheckoutCreationIsTerminal(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.platform.CancelOrder(ctx, "t1", ports.CancelOrderRequest{
		ExternalCheckoutID: string(c.ID) + ":t1",
		GroupID:            "t1",
		Reason:             "buyer_cancelled",
	}); err != nil {
		t.Fatal(err)
	}
	state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
	if err != nil || !state.Groups[0].CreateExhausted || state.Groups[0].Status != checkout.GroupRejected ||
		state.Groups[0].ReconciliationRequired || state.Groups[0].Reason != "ORDER_CHECKOUT_CANCELLED" {
		t.Fatal(state, err)
	}
	e.clock.Advance(time.Second)
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	state, err = e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].CreateFailures != 1 {
		t.Fatal(state, err)
	}
}

func TestCancellationRetriesAndDoesNotBlockHealthySeller(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Cancel(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	e.platform.FailCancel("t1", &ports.OrderRefusal{Code: "MARKETPLACE_TEMPORARY_FAILURE"})
	started, release := make(chan struct{}, 1), make(chan struct{})
	platform := delayedCancellation{PlatformAPI: e.platform, started: started, release: release}
	service := appcheckout.NewService(e.store, platform, nil, nil, &fake.IDs{}, e.clock, appcheckout.Config{})
	executor := appcheckout.NewExecutor(e.store, e.clock, service, nil, messaging.DefaultMaxAttempts)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	<-started
	result := <-executor.Results()
	if result.Task.TenantID != "t2" || result.Err != nil {
		t.Fatal(result)
	}
	close(release)
	result = <-executor.Results()
	executor.Wait()
	if result.Err == nil {
		t.Fatal("503 должен отложить команду")
	}
	// Повтор Cancel во время паузы не создаёт новую задачу, обходящую backoff.
	if _, err := e.service.Cancel(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	counts := e.store.ReadyTasks(e.clock.Now())
	if counts[appcheckout.TaskCancelOrder] != 0 {
		t.Fatal(counts)
	}
	e.platform.FailCancel("t1", nil)
	e.clock.Advance(time.Second)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	result = <-executor.Results()
	executor.Wait()
	if result.Err != nil {
		t.Fatal(result)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || !state.Groups[0].CancellationConfirmed || len(e.platform.CancellationRequests("t1")) != 2 ||
		state.Groups[0].CancellationAttempts != 2 || state.Groups[0].CancellationLastErrorCode != "" {
		t.Fatal(state, err)
	}
}

func TestCancellationTaskAndTransitionRollbackTogether(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("постановка недоступна")
	uow := failingEnqueueUOW{UnitOfWork: e.store, failure: failure}
	service := appcheckout.NewService(uow, e.platform, nil, nil, &fake.IDs{}, e.clock, appcheckout.Config{})
	if _, err := service.Cancel(ctx, buyerID, c.ID); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusAwaitingPayment || state.Groups[0].CancellationQueued {
		t.Fatal(state, err)
	}
	for _, row := range e.store.OutboxRows() {
		if row.Kind == appcheckout.TaskCancelOrder {
			t.Fatal("задача пережила откат", row)
		}
	}
	basket, err := e.carts.Get(ctx, buyerID)
	if err != nil || len(basket.Lines) != 0 {
		t.Fatal(basket, err)
	}
}

func TestCancellationNetworkFailureRetries(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Cancel(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
	e.platform.FailCancel("t1", context.DeadlineExceeded)
	executor := appcheckout.NewExecutor(e.store, e.clock, e.service, nil, messaging.DefaultMaxAttempts)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result := <-executor.Results()
		if result.Task.TenantID == "t1" && !errors.Is(result.Err, context.DeadlineExceeded) {
			t.Fatal(result)
		}
		if result.Task.TenantID == "t2" && result.Err != nil {
			t.Fatal(result)
		}
	}
	executor.Wait()
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || !state.Groups[0].ReconciliationRequired || state.Groups[0].CancellationConfirmed ||
		state.Groups[0].CancellationAttempts != 1 ||
		state.Groups[0].CancellationLastErrorCode != "platform.unavailable" {
		t.Fatal(state, err)
	}
	e.platform.FailCancel("t1", nil)
	e.clock.Advance(time.Second)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	result := <-executor.Results()
	executor.Wait()
	if result.Err != nil || len(e.platform.CancellationRequests("t1")) != 2 {
		t.Fatal(result)
	}
}

func TestCancellationInvalidResultStopsRetries(t *testing.T) {
	cases := []struct {
		name    string
		answer  ports.CancelOrderResult
		failure error
		code    string
	}{
		{"mismatch", ports.CancelOrderResult{OrderID: "another-order"}, nil, "checkout.cancellation_order_mismatch"},
		{"absent", ports.CancelOrderResult{Absent: true}, nil, "checkout.cancellation_order_absent"},
		{
			"invalid",
			ports.CancelOrderResult{},
			ports.ErrInvalidCancellationResponse,
			"platform.invalid_cancellation_response",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			c := confirmedPurchase(t, e)
			ctx := context.Background()
			before, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
			if err != nil {
				t.Fatal(err)
			}
			platform := cancellationAnswer{PlatformAPI: e.platform, answer: tc.answer, failure: tc.failure}
			e.service = appcheckout.NewService(
				e.store,
				platform,
				nil,
				nil,
				&fake.IDs{},
				e.clock,
				appcheckout.Config{},
			)
			if _, err := e.service.Cancel(ctx, buyerID, c.ID); err != nil {
				t.Fatal(err)
			}
			if err := executeTasks(ctx, e); err != nil {
				t.Fatal(err)
			}
			e.clock.Advance(time.Minute)
			if err := executeTasks(ctx, e); err != nil {
				t.Fatal(err)
			}
			states, err := e.service.OperatorList(ctx, "", true)
			if err != nil || len(states) != 1 {
				t.Fatal(states, err)
			}
			g := states[0].Groups[0]
			if g.CancellationAttempts != 1 || g.CancellationLastErrorCode != tc.code ||
				!g.ReconciliationRequired || !g.CancellationBlocked || g.OrderID != before.Groups[0].OrderID {
				t.Fatal(g)
			}
		},
		)
	}
}

func TestCancellationRefusalReconciliationEndsAtTerminalOrder(t *testing.T) {
	for _, terminal := range []checkout.GroupStatus{
		checkout.GroupDelivered,
		checkout.GroupReturned,
		checkout.GroupCancelled,
	} {
		t.Run(string(terminal), func(t *testing.T) {
			e := setup(t)
			c := confirmedPurchase(t, e)
			ctx := context.Background()
			state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
			if err != nil {
				t.Fatal(err)
			}
			id := state.Groups[0].OrderID
			e.platform.SetOrderStatus(id, checkout.GroupPaid, "")
			if _, err := e.service.Cancel(ctx, buyerID, c.ID); err != nil {
				t.Fatal(err)
			}
			if err := executeTasks(ctx, e); err != nil {
				t.Fatal(err)
			}
			e.platform.SetOrderStatus(id, terminal, "terminal")
			if err := resumeAndExecute(ctx, e); err != nil {
				t.Fatal(err)
			}
			state, err = e.service.Get(ctx, buyerID, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			g := state.Groups[0]
			if g.ReconciliationRequired || g.LastOrderStatus != terminal || g.PollDue(e.clock.Now().Add(time.Hour)) ||
				g.CancellationLastErrorCode != "ORDER_NOT_CANCELLABLE" || g.CancellationAttempts != 1 {
				t.Fatal(g)
			}
			stuck, err := e.service.OperatorList(ctx, "", true)
			if err != nil || len(stuck) != 0 {
				t.Fatal(stuck, err)
			}
		})
	}
}
