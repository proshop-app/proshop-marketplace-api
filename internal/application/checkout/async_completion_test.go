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
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// Сбой включается после ответа магазина, чтобы проверять именно запись итога.
type resultUOW struct {
	ports.UnitOfWork
	failure   error
	remaining int
	attempts  []time.Time
}

type brokenCartUOW struct{ ports.UnitOfWork }
type brokenCartTx struct{ ports.Tx }
type brokenCartRepository struct{ cart.Repository }

func (u *resultUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if u.remaining > 0 {
		u.remaining--
		u.attempts = append(u.attempts, time.Now())
		return u.failure
	}
	return u.UnitOfWork.Do(ctx, fn)
}

func (u brokenCartUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return u.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return fn(ctx, brokenCartTx{Tx: tx})
	})
}

func (tx brokenCartTx) Carts() cart.Repository {
	return brokenCartRepository{Repository: tx.Tx.Carts()}
}

func (r brokenCartRepository) Save(context.Context, *cart.Cart) error {
	return errs.New(errs.Unavailable, "test.cart", "корзина недоступна")
}

func firstCreationTask(t *testing.T, e *env) ports.Task {
	t.Helper()
	tasks, err := e.store.Claim(context.Background(), ports.ClaimRequest{
		Now:        e.clock.Now(),
		Limit:      1,
		PerTenant:  1,
		Kinds:      []string{appcheckout.TaskCreateOrder},
		LeaseUntil: e.clock.Now().Add(10 * time.Second),
	})
	if err != nil || len(tasks) != 1 {
		t.Fatal(tasks, err)
	}
	return tasks[0]
}

func TestCreationSaveRetriesOnlyTransientFailures(t *testing.T) {
	for _, failure := range []error{
		checkout.ErrConflict,
		errs.New(errs.Unavailable, "test.storage", "хранилище недоступно"),
		errs.New(errs.Invalid, "test.invalid", "неверные данные"),
		errors.New("неожиданная ошибка"),
	} {
		t.Run(failure.Error(), func(t *testing.T) {
			e := setup(t)
			c := confirmedPurchase(t, e)
			ctx := context.Background()
			if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
				t.Fatal(err)
			}
			task := firstCreationTask(t, e)
			uow := &resultUOW{UnitOfWork: e.store, failure: failure}
			service := appcheckout.NewService(
				uow,
				e.platform,
				nil,
				nil,
				&fake.IDs{},
				e.clock,
				appcheckout.Config{},
			)
			e.platform.AfterCreate = func() { uow.remaining = 3 }
			err := service.HandleOrderTask(ctx, task)
			retryable := errors.Is(failure, checkout.ErrConflict) || errs.CodeOf(failure) == errs.Unavailable
			if !retryable {
				if !errors.Is(err, failure) || len(uow.attempts) != 1 {
					t.Fatal(err, uow.attempts)
				}
				return
			}
			if err != nil || len(uow.attempts) != 3 {
				t.Fatal(err, uow.attempts)
			}
			for i := 1; i < len(uow.attempts); i++ {
				minimum := time.Duration(1<<(i-1)) * 20 * time.Millisecond
				if uow.attempts[i].Sub(uow.attempts[i-1]) < minimum {
					t.Fatal("повтор без нарастающей паузы")
				}
			}
			state, err := e.service.Get(ctx, buyerID, c.ID)
			if err != nil || state.Groups[0].OrderID == "" || len(e.platform.OrderRequests("t1")) != 1 {
				t.Fatal(state, err)
			}
		},
		)
	}
}

func TestSuccessfulCreationPersistsAfterShutdown(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	task := firstCreationTask(t, e)
	uow := &resultUOW{UnitOfWork: e.store}
	service := appcheckout.NewService(
		uow,
		e.platform,
		nil,
		nil,
		&fake.IDs{},
		e.clock,
		appcheckout.Config{},
	)
	e.platform.AfterCreate = cancel
	if err := service.HandleOrderTask(ctx, task); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	state, err := e.service.Get(context.Background(), buyerID, c.ID)
	if err != nil || state.Groups[0].OrderID == "" || state.Groups[0].Status != checkout.GroupAwaitingPayment {
		t.Fatal("полученный orderId потерян при остановке", state, err)
	}
}

func TestDeadlineCommitsDespiteCartFailure(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.service = appcheckout.NewService(
		brokenCartUOW{UnitOfWork: e.store},
		e.platform,
		nil,
		nil,
		&fake.IDs{},
		e.clock,
		appcheckout.Config{},
	)
	e.clock.Advance(appcheckout.CreationTimeout)
	if err := executeTasks(ctx, e); err != nil {
		t.Fatal(err)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Status != checkout.StatusDraft || checkout.Restore(state).CreationComplete() {
		t.Fatal(state, err)
	}
	for _, g := range state.Groups {
		if g.Status != checkout.GroupCreationExpired || g.ReturnFailed || g.CartReturned || g.Excluded() {
			t.Fatal(g)
		}
	}
}

func TestDeadlineTaskWaitsForAttemptLease(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	if _, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(appcheckout.CreationTimeout - time.Second)
	task := firstCreationTask(t, e)
	lease := e.clock.Now().Add(time.Second + 100*time.Millisecond)
	if err := e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		current, err := tx.Checkouts().Get(ctx, c.ID)
		if err != nil {
			return err
		}
		if _, err := current.ClaimCreation(task.TenantID, "active", lease, e.clock.Now()); err != nil {
			return err
		}
		return tx.Checkouts().Update(ctx, current)
	}); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Second)
	executor := appcheckout.NewExecutor(e.store, e.clock, e.service, messaging.DefaultMaxAttempts)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		result := <-executor.Results()
		if result.Task.Kind == appcheckout.TaskExpireCreation && result.Task.TenantID == task.TenantID {
			if !appcheckout.CreationPending(result.Err) {
				t.Fatal(result)
			}
		} else if result.Err != nil {
			t.Fatal(result)
		}
	}
	executor.Wait()
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].Abandoned {
		t.Fatal(state, err)
	}
	for _, row := range e.store.OutboxRows() {
		if row.Kind == appcheckout.TaskExpireCreation && row.TenantID == task.TenantID && !row.NextAt.Equal(lease) {
			t.Fatal("закрытие должно повториться в конце аренды", row)
		}
	}
	e.clock.Advance(100 * time.Millisecond)
	if err := executor.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	result := <-executor.Results()
	executor.Wait()
	if result.Err != nil || result.Task.Kind != appcheckout.TaskExpireCreation {
		t.Fatal(result)
	}
	state, err = e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].Status != checkout.GroupCreationExpired {
		t.Fatal(state, err)
	}
}

func TestPaymentCompletionSurvivesClientAndClearsOnlyProvenUnpaid(t *testing.T) {
	for _, previousPaid := range []bool{false, true} {
		e := setup(t)
		c := confirmedPurchase(t, e)
		ctx := context.Background()
		state, err := beginAndExecute(ctx, e, buyerID, c.ID, orderContact)
		if err != nil {
			t.Fatal(err)
		}
		if previousPaid {
			e.platform.SetOrderStatus(state.Groups[1].OrderID, checkout.GroupPaid, "")
		}
		if err := e.store.Do(
			ctx,
			func(ctx context.Context, tx ports.Tx) error {
				current, err := tx.Checkouts().Get(ctx, c.ID)
				if err != nil {
					return err
				}
				if err := current.ClaimPayment("uncertain", current.PaymentOrders(), e.clock.Now()); err != nil {
					return err
				}
				return tx.Checkouts().Update(ctx, current)
			},
		); err != nil {
			t.Fatal(err)
		}
		requestCtx, cancel := context.WithCancel(ctx)
		e.platform.CallHook = func(ctx context.Context, operation string) error {
			if operation == "stub_payment" {
				cancel()
				return &ports.OrderRefusal{Code: "MARKETPLACE_STUB_PAYMENT_AMOUNT"}
			}
			return ctx.Err()
		}
		uow := &resultUOW{UnitOfWork: e.store}
		service := appcheckout.NewService(
			uow,
			e.platform,
			nil,
			nil,
			&fake.IDs{},
			e.clock,
			appcheckout.Config{
				PaymentStub: true,
			},
		)
		err = service.ConfirmPayment(requestCtx, buyerID, c.ID)
		cancel()
		if !errors.Is(err, checkout.ErrTransition) {
			t.Fatal(err)
		}
		state, err = service.Get(ctx, buyerID, c.ID)
		if err != nil || !state.PaymentStarted || state.PaymentAttempt != "" {
			t.Fatal("завершение попытки должно пережить обрыв клиента", state, err)
		}
		_, err = service.Cancel(ctx, buyerID, c.ID)
		if !errors.Is(err, checkout.ErrTransition) {
			t.Fatal(err)
		}
	}
}

func TestExecutorBacksOffNonPendingFailures(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if err := e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().Enqueue(ctx, ports.NewTask{
			Kind:     appcheckout.TaskCreateOrder,
			TenantID: "t1",
			Payload:  []byte(`{}`),
			At:       e.clock.Now(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	unavailable := errs.New(errs.Unavailable, "test.storage", "база недоступна")
	uow := &resultUOW{UnitOfWork: e.store, failure: unavailable, remaining: 100}
	service := appcheckout.NewService(uow, e.platform, nil, nil, &fake.IDs{}, e.clock, appcheckout.Config{})
	executor := appcheckout.NewExecutor(e.store, e.clock, service, messaging.DefaultMaxAttempts)
	for _, delay := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		now := e.clock.Now()
		if err := executor.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		result := <-executor.Results()
		executor.Wait()
		if result.Err == nil || appcheckout.CreationPending(result.Err) {
			t.Fatal(result)
		}
		for _, row := range e.store.OutboxRows() {
			if row.Kind == appcheckout.TaskCreateOrder && !row.NextAt.Equal(now.Add(delay)) {
				t.Fatal(row, delay)
			}
		}
		e.clock.Advance(delay)
	}
}
