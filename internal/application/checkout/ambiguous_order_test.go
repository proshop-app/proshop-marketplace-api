package checkout_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Потерянный успешный ответ и последующий окончательный отказ требуют отмены возможного заказа.
type rejectAfterLostReply struct {
	ports.PlatformAPI
	calls atomic.Int32
}

func (p *rejectAfterLostReply) CreateOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	if tenant != "t2" {
		return p.PlatformAPI.CreateOrder(ctx, tenant, request)
	}
	if p.calls.Add(1) == 1 {
		_, _ = p.PlatformAPI.CreateOrder(ctx, tenant, request)
		return checkout.SellerOrder{}, context.DeadlineExceeded
	}
	return checkout.SellerOrder{}, &ports.OrderRefusal{Code: "DELIVERY_CHANGED"}
}

func runTasks(ctx context.Context, e *env, svc *appcheckout.Service) error {
	tasks, err := e.store.Claim(
		ctx,
		ports.ClaimRequest{
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
		},
	)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		err := svc.HandleOrderTask(ctx, task)
		if err != nil {
			next := e.clock.Now().Add(checkout.CreationRetryDelay)
			if err := e.store.Retry(ctx, task.ID, task.Attempts, next, err.Error()); err != nil {
				return err
			}
			continue
		}
		if err := e.store.Complete(ctx, task.ID, task.Attempts, e.clock.Now()); err != nil {
			return err
		}
	}
	return nil
}

func TestRefusalAfterLostReplyCancelsPossibleOrder(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	upstream := &rejectAfterLostReply{PlatformAPI: e.platform}
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
	for range 3 {
		if err := runTasks(ctx, e, svc); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(2 * time.Second)
	}
	after, err := svc.Get(ctx, buyerID, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	logGroups(t, after)
	cancelled, cancelErr := svc.Cancel(ctx, buyerID, state.ID)
	for range 3 {
		if err := runTasks(ctx, e, svc); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf(
		"заказов на сервере: %d; Cancel: %v status=%s; cancel-by-checkout t2: %d",
		e.platform.OrderCount(),
		cancelErr,
		cancelled.Status,
		len(e.platform.CancellationRequests("t2")),
	)
	for _, g := range after.Groups {
		if g.TenantID == "t2" && !g.ReconciliationRequired && len(e.platform.CancellationRequests("t2")) == 0 {
			t.Fatalf("созданный при потере ответа заказ t2 остаётся на сервере без сверки")
		}
	}
}
