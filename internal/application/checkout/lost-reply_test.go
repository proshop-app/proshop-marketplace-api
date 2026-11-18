package checkout_test

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/platformguard"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type lostCreationReply struct {
	ports.PlatformAPI
	lost atomic.Bool
}

func (p *lostCreationReply) CreateOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	answer, err := p.PlatformAPI.CreateOrder(ctx, tenant, request)
	if err == nil && p.lost.CompareAndSwap(false, true) {
		<-ctx.Done()
		return checkout.SellerOrder{}, ctx.Err()
	}
	return answer, err
}

func checkoutWithLostReply(e *env) *appcheckout.Service {
	upstream := &lostCreationReply{PlatformAPI: e.platform}
	guard := platformguard.New(
		upstream,
		platformguard.Config{
			StrictTimeout:         20 * time.Millisecond,
			MaxConcurrent:         4,
			CheckoutMaxConcurrent: 2,
			ReadMaxConcurrent:     2,
			TenantMaxConcurrent:   2,
			QueueSize:             8,
			TenantQueueSize:       2,
			TenantStateSize:       100,
			TenantIdleTTL:         time.Minute,
			FailureThreshold:      5,
			FailureWindow:         30 * time.Second,
			FailureMinCalls:       20,
			FailurePercent:        50,
			OpenInterval:          time.Second,
		},
		e.clock,
		nil,
		nil,
	)
	return appcheckout.NewService(
		e.store,
		guard,
		nil,
		nil,
		&fake.IDs{},
		e.clock,
		appcheckout.Config{PaymentStub: true, OrderCallTimeout: time.Second},
	)
}

func TestCreationTimeoutKeepsIntentAndSellerKeyOnRetry(t *testing.T) {
	e := setup(t)
	c := confirmedPurchase(t, e)
	ctx := context.Background()
	intent, err := e.service.CreateOrders(ctx, buyerID, c.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	task := firstCreationTask(t, e)
	e.service = checkoutWithLostReply(e)
	if err := e.service.HandleOrderTask(ctx, task); !appcheckout.CreationPending(err) {
		t.Fatal("потерянный ответ должен оставить фоновый повтор", err)
	}
	requests := e.platform.OrderRequests(task.TenantID)
	if e.platform.OrderCount() != 1 || len(requests) != 1 {
		t.Fatal("первый запрос должен создать заказ у магазина", requests)
	}
	repeated, err := e.service.Create(ctx, buyerID, key)
	if err != nil || repeated.ID != c.ID {
		t.Fatal("повтор покупателя сменил checkout", repeated, err)
	}
	repeated, err = e.service.CreateOrders(ctx, buyerID, c.ID, checkout.Contact{})
	if err != nil || repeated.Groups[0].ExternalCheckoutID != intent.Groups[0].ExternalCheckoutID {
		t.Fatal("повтор оформления сменил ключ продавца", repeated, err)
	}
	e.clock.Advance(checkout.CreationRetryDelay)
	if err := e.service.HandleOrderTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	requests = e.platform.OrderRequests(task.TenantID)
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) || e.platform.OrderCount() != 1 {
		t.Fatal("повтор изменил снимок или создал второй заказ", requests)
	}
	state, err := e.service.Get(ctx, buyerID, c.ID)
	if err != nil || state.Groups[0].OrderID != "order-1" || state.Groups[0].Status != checkout.GroupAwaitingPayment {
		t.Fatal("повтор не восстановил первый заказ", state, err)
	}
}
