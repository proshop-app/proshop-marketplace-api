package checkout_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type failingEnqueueUOW struct {
	ports.UnitOfWork
	failure error
}

type failingEnqueueTx struct {
	ports.Tx
	failure error
}

type failingEnqueueOutbox struct {
	ports.Outbox
	failure error
}

// Промежуток между ответом магазина и чтением строки для записи итога.
type delayedCommitUOW struct {
	ports.UnitOfWork
	clock   *fake.Clock
	advance bool
}

func (u *delayedCommitUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	if u.advance {
		u.advance = false
		u.clock.Advance(110 * time.Millisecond)
	}
	return u.UnitOfWork.Do(ctx, fn)
}

func (u failingEnqueueUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return u.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return fn(ctx, failingEnqueueTx{Tx: tx, failure: u.failure})
	})
}

func (tx failingEnqueueTx) Outbox() ports.Outbox {
	return failingEnqueueOutbox{Outbox: tx.Tx.Outbox(), failure: tx.failure}
}

func (o failingEnqueueOutbox) Enqueue(ctx context.Context, task ports.NewTask) error {
	if err := o.Outbox.Enqueue(ctx, task); err != nil {
		return err
	}
	return o.failure
}

func executeTasks(ctx context.Context, e *env) error {
	tasks, err := e.store.Claim(ctx, ports.ClaimRequest{
		Now:        e.clock.Now(),
		Limit:      200,
		PerTenant:  200,
		LeaseUntil: e.clock.Now().Add(5*time.Second + 100*time.Millisecond),
		Kinds: []string{
			appcheckout.TaskCreateOrder,
			appcheckout.TaskExpireCreation,
			appcheckout.TaskPollOrder,
			appcheckout.TaskCancelOrder,
		},
	})
	if err != nil {
		return err
	}
	for _, task := range tasks {
		err := e.service.HandleOrderTask(ctx, task)
		if typed, ok := errs.As(err); ok && typed.Key() == "checkout.creation_pending" {
			next := e.clock.Now().Add(checkout.CreationRetryDelay)
			if err := e.store.Retry(ctx, task.ID, task.Attempts, next, err.Error()); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := e.store.Complete(ctx, task.ID, task.Attempts, e.clock.Now()); err != nil {
			return err
		}
	}
	return nil
}

func beginAndExecute(
	ctx context.Context,
	e *env,
	buyer, id ids.UUID,
	contact checkout.Contact,
) (checkout.State, error) {
	state, err := e.service.CreateOrders(ctx, buyer, id, contact)
	if err != nil {
		return state, err
	}
	if err := executeTasks(ctx, e); err != nil {
		return state, err
	}
	return e.service.Get(ctx, buyer, id)
}

func resumeAndExecute(ctx context.Context, e *env) error {
	if err := e.service.Resume(ctx); err != nil {
		return err
	}
	return executeTasks(ctx, e)
}

func TestAsyncIntentAndDeadline(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	state, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact)
	if err != nil || state.Status != checkout.StatusCreatingOrders || !state.CartConsumed {
		t.Fatal(state, err)
	}
	if e.platform.OrderCount() != 0 || checkout.Restore(state).CreationComplete() {
		t.Fatal("HTTP не должен создавать заказы")
	}
	rows := len(e.store.OutboxRows())
	repeated, err := e.service.CreateOrders(ctx, buyerID, c.ID, checkout.Contact{})
	if err != nil || repeated.Version != state.Version || rows != len(e.store.OutboxRows()) {
		t.Fatal("повтор изменил намерение", repeated, err)
	}
	if _, err := e.service.Pay(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal("оплата до итога", err)
	}
	purchases, err := e.service.Purchases(ctx, buyerID, "")
	if err != nil || len(purchases) != 0 {
		t.Fatal("намерение без заказов не документ", purchases, err)
	}
	e.clock.Advance(15 * time.Second)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	state, err = e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusDraft || checkout.Restore(state).CreationComplete() ||
		e.platform.OrderCount() != 0 {
		t.Fatal(state, err)
	}
	for _, g := range state.Groups {
		if g.Status != checkout.GroupSellerUnreachable || g.CartReturned || g.ReconciliationRequired || g.Excluded() {
			t.Fatal(g)
		}
	}
}

func TestAsyncSuccessBeforeDeadlineClosure(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	uow := &delayedCommitUOW{UnitOfWork: e.store, clock: e.clock}
	e.service = appcheckout.NewService(uow, e.platform, nil, nil, &fake.IDs{}, e.clock, appcheckout.Config{})
	e.platform.AfterCreate = func() { e.clock.Advance(14900 * time.Millisecond); uow.advance = true }
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].Status != checkout.GroupAwaitingPayment || state.Groups[0].OrderID == "" {
		t.Fatal("успех принимается, пока группа не закрыта", state, err)
	}

}

func TestAsyncPartialCancelAndCabinet(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	e.platform.FailCreate("t1", errors.New("нет ответа"))
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	waiting, err := e.service.Purchases(ctx, buyerID, "")
	if err != nil || len(waiting) != 1 || waiting[0].State().Status != checkout.StatusAwaitingPayment ||
		checkout.Restore(waiting[0].State()).CreationComplete() {
		t.Fatal("неоплаченный заказ должен быть виден до завершения остальных групп", waiting, err)
	}
	e.clock.Advance(15 * time.Second)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Pay(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal("неизменённая покупка с временным сбоем не допускает частичной оплаты", err)
	}
	purchases, err := e.service.Purchases(ctx, buyerID, "")
	if err != nil || len(purchases) != 1 || purchases[0].State().Status != checkout.StatusDraft {
		t.Fatal(purchases, err)
	}
	state, err := e.service.Cancel(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusCancelled || !state.Groups[1].ReconciliationRequired {
		t.Fatal(state, err)
	}
	version := state.Version
	state, err = e.service.Cancel(ctx, buyerID, c.ID)
	if err != nil || state.Version != version {
		t.Fatal("повтор отмены", state, err)
	}
	if _, err := e.service.Pay(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal(err)
	}
}

func refreshOrders(ctx context.Context, e *env, buyer, id ids.UUID) (checkout.State, error) {
	if _, err := e.service.Get(ctx, buyer, id); err != nil {
		return checkout.State{}, err
	}
	if err := resumeAndExecute(ctx, e); err != nil {
		return checkout.State{}, err
	}
	return e.service.Get(ctx, buyer, id)
}

func TestAsyncIntentRollback(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	before, err := e.carts.Get(ctx, buyerID)
	if err != nil {
		t.Fatal(err)
	}
	rows := len(e.store.OutboxRows())
	failure := errors.New("фиксация недоступна")
	uow := failingEnqueueUOW{UnitOfWork: e.store, failure: failure}
	cfg := appcheckout.Config{
		PaymentStub:  true,
		QuoteTTL:     30 * time.Minute,
		QuoteTimeout: time.Second,
	}
	service := appcheckout.NewService(uow, e.platform, nil, nil, &fake.IDs{}, e.clock, cfg)
	if _, err := service.CreateOrders(ctx, buyerID, c.ID, orderContact); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusConfirmed || state.CartConsumed ||
		len(e.store.OutboxRows()) != rows {
		t.Fatal(state, err)
	}
	after, err := e.carts.Get(ctx, buyerID)
	if err != nil || len(after.Lines) != len(before.Lines) || after.Lines[0].Quantity != before.Lines[0].Quantity {
		t.Fatal(after, err)
	}
}

func TestAsyncTaskReplay(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	first, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	// Повторная доставка уже выполненной задачи проверяет обработчик, а не только done_at очереди.
	for _, row := range e.store.OutboxRows() {
		if row.Kind != appcheckout.TaskCreateOrder {
			continue
		}
		if err := e.service.HandleOrderTask(ctx, ports.Task{
			Kind:     row.Kind,
			TenantID: row.TenantID,
			Payload:  row.Payload,
		}); err != nil {
			t.Fatal(err)
		}
	}
	second, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || first.Version != second.Version || e.platform.OrderCount() != 2 ||
		len(e.platform.OrderRequests("t1")) != 1 {
		t.Fatal(second, err)
	}
}

func TestCancelWhileCreationIsInFlight(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.platform.AfterCreate = func() {
		if _, err := e.service.Cancel(ctx, buyerID, c.ID); err != nil {
			t.Error(err)
		}
	}
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusCancelled || state.Groups[0].OrderID == "" ||
		!state.Groups[0].ReconciliationRequired || e.platform.OrderCount() != 1 {
		t.Fatal(state, err)
	}
	if _, err := e.service.Pay(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal(err)
	}
}

func TestCancelCannotRacePayment(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.platform.AfterStubPayment = func() {
		if _, err := e.service.Cancel(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrTransition) {
			t.Error(err)
		}
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentCreateOrdersReturnsSameIntent(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan checkout.State, 2)
	failures := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			state, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact)
			results <- state
			failures <- err
		}()
	}
	close(start)
	first, second := <-results, <-results
	if err := <-failures; err != nil {
		t.Fatal(err)
	}
	if err := <-failures; err != nil {
		t.Fatal(err)
	}
	if first.Version != second.Version || first.Status != checkout.StatusCreatingOrders {
		t.Fatal(first, second)
	}
	count := 0
	for _, row := range e.store.OutboxRows() {
		if row.Kind == appcheckout.TaskCreateOrder || row.Kind == appcheckout.TaskExpireCreation {
			count++
		}
	}
	if count != 4 {
		t.Fatal("на каждую группу создание и срок ровно один раз", count)
	}
}

func TestDefinitiveFirstPaymentRefusalAllowsCancellation(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.platform.Fail("t1", &ports.OrderRefusal{Code: "MARKETPLACE_STUB_PAYMENT_AMOUNT"})
	if err := e.service.ConfirmPayment(ctx, buyerID, c.ID); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal(err)
	}
	state, err := e.service.Cancel(ctx, buyerID, c.ID)
	if err != nil || state.PaymentStarted || state.Status != checkout.StatusCancelled {
		t.Fatal(state, err)
	}
}

func TestExecutorRetriesRespectDeadline(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	e.platform.FailCreate("t1", errors.New("нет ответа"))
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	executor := appcheckout.NewExecutor(e.store, e.clock, e.service)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		<-executor.Results()
	}
	executor.Wait()
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	executor.Wait()
	if len(e.platform.OrderRequests("t1")) != 1 {
		t.Fatal("повтор без паузы")
	}
	e.clock.Advance(time.Second)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	result := <-executor.Results()
	if result.Err == nil {
		t.Fatal("ошибка магазина должна откладывать задачу")
	}
	executor.Wait()
	e.clock.Advance(14 * time.Second)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		result := <-executor.Results()
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	executor.Wait()
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusDraft || state.Groups[0].Excluded() ||
		len(e.platform.OrderRequests("t1")) != 2 || state.Groups[0].CartReturned {
		t.Fatal(state, err)
	}
}

func TestCreationResultSurvivesMoreThanThreeCASConflicts(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	uow := &paymentConflictUOW{UnitOfWork: e.store}
	e.service = appcheckout.NewService(uow, e.platform, nil, nil, &fake.IDs{}, e.clock, appcheckout.Config{})
	e.platform.AfterCreate = func() { uow.remaining = 5 }
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusAwaitingPayment || uow.attempts != 10 ||
		len(e.platform.OrderRequests("t1")) != 1 || len(e.platform.OrderRequests("t2")) != 1 {
		t.Fatal(state, err)
	}
}

func TestCancellationRetriesCAS(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	uow := &paymentConflictUOW{UnitOfWork: e.store, remaining: 5}
	service := appcheckout.NewService(uow, e.platform, nil, nil, &fake.IDs{}, e.clock, appcheckout.Config{})
	state, err := service.Cancel(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusCancelled || uow.attempts != 5 {
		t.Fatal(state, err)
	}
	for _, g := range state.Groups {
		if !g.CartReturned {
			t.Fatal(g)
		}
	}
}

func TestCreationLeaseRespectsShorterTransportTimeout(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.service = appcheckout.NewService(
		e.store,
		e.platform,
		nil,
		nil,
		&fake.IDs{},
		e.clock,
		appcheckout.Config{
			OrderCallTimeout: time.Second,
		},
	)
	e.platform.AfterCreate = func() {
		state, err := e.service.Get(ctx, buyerID, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range state.Groups {
			if g.CreateToken != "" && g.NextCreateAt.Sub(e.clock.Now()) != time.Second+100*time.Millisecond {
				t.Fatal("аренда должна учитывать бюджет адаптера", g.NextCreateAt)
			}
		}
	}
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
}
