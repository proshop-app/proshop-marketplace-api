package checkout_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	"github.com/proshop-app/proshop-marketplace/internal/application/platformguard"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type creationLoad struct {
	guard     *platformguard.Client
	metrics   *fake.PlatformMetrics
	queue     *leaseQueue
	executor  *appcheckout.Executor
	deadline  time.Time
	checkouts []ids.UUID
	store     *fake.Store
}

type scheduledCreationPlatform struct {
	*fake.Platform
	silent  map[ids.TenantID]bool
	started chan ids.TenantID
	quotes  chan struct{}
}

func (p *scheduledCreationPlatform) CreateOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	if p.silent[tenant] {
		p.started <- tenant
		<-ctx.Done()
		return checkout.SellerOrder{}, ctx.Err()
	}
	return p.Platform.CreateOrder(ctx, tenant, request)
}

func (p *scheduledCreationPlatform) QuoteDelivery(
	ctx context.Context,
	_ ids.TenantID,
	_ ports.QuoteRequest,
) (delivery.Offered, error) {
	p.quotes <- struct{}{}
	<-ctx.Done()
	return delivery.Offered{}, ctx.Err()
}

func TestExecutorCreationWithConcurrentDeliveryQuotes(t *testing.T) {
	for _, shoppers := range []int{3, 6} {
		t.Run(fmt.Sprint(shoppers), func(t *testing.T) {
			clock := fake.NewClock(t0).Scheduler()
			upstream := &scheduledCreationPlatform{
				Platform: fake.NewPlatform(),
				quotes:   make(chan struct{}, shoppers),
			}
			upstream.OrderClock = clock
			plans := make([][]ids.TenantID, 24)
			for i := range plans {
				plans[i] = []ids.TenantID{"t1", "t2"}
			}
			load := prepareCreationLoad(t, upstream, clock, plans)
			ctx, cancel := context.WithCancel(context.Background())
			quotesDone := make(chan struct{}, shoppers)
			defer func() {
				cancel()
				for range shoppers {
					<-quotesDone
				}
				load.executor.Wait()
			}()
			quoteShops := []ids.TenantID{"t1", "t3", "t4"}
			for i := range shoppers {
				tenant := quoteShops[i%len(quoteShops)]
				go func() {
					_, _ = load.guard.QuoteDelivery(ctx, tenant, ports.QuoteRequest{})
					quotesDone <- struct{}{}
				}()
			}
			for range shoppers {
				<-upstream.quotes
			}
			load.dispatch(ctx, t)
			load.finishHealthy(ctx, t, 2*len(plans))
			if got := load.metrics.CountClass("create_order", "checkout", "ok"); got != 2*len(plans) {
				t.Fatal("вызовы создания потеряли класс checkout", got)
			}
			if got := load.metrics.Count("create_order", "queue_full"); got != 0 {
				t.Fatal("расчёты заняли очередь создания", got)
			}
			select {
			case <-quotesDone:
				quotesDone <- struct{}{}
				t.Fatal("создание должно завершиться при незавершённых расчётах")
			default:
			}
		})
	}
}

func TestExecutorCreationWithSilentShops(t *testing.T) {
	for _, tc := range []struct {
		shops  int
		silent int
	}{
		{shops: 32, silent: 2},
		{shops: 48, silent: 48},
	} {
		t.Run(fmt.Sprintf("%d_из_%d", tc.silent, tc.shops), func(t *testing.T) {
			clock := fake.NewClock(t0).Scheduler()
			upstream := &scheduledCreationPlatform{
				Platform: fake.NewPlatform(),
				silent:   map[ids.TenantID]bool{},
				started:  make(chan ids.TenantID, 2*tc.shops),
			}
			upstream.OrderClock = clock
			plans := make([][]ids.TenantID, 0, 2*tc.shops)
			for i := range tc.shops {
				tenant := ids.TenantID(fmt.Sprintf("t%03d", i))
				if i < tc.silent {
					upstream.silent[tenant] = true
				}
				plans = append(plans, []ids.TenantID{tenant})
				if !upstream.silent[tenant] {
					plans = append(plans, []ids.TenantID{tenant})
				}
			}
			load := prepareCreationLoad(t, upstream, clock, plans)
			ctx, cancel := context.WithCancel(context.Background())
			defer func() {
				cancel()
				load.executor.Wait()
			}()
			healthy := 2 * (tc.shops - tc.silent)
			if healthy == 0 {
				testSilentCreationServer(ctx, t, load, clock, upstream)
				return
			}

			load.dispatch(ctx, t)
			for range tc.silent {
				<-upstream.started
			}
			load.finishHealthy(ctx, t, healthy)
			clock.Advance(appcheckout.CreationCallTimeout)
			for range tc.silent {
				if result := <-load.executor.Results(); !appcheckout.CreationPending(result.Err) {
					t.Fatal("молчание магазина должно оставить повтор", result)
				}
			}
			if load.guard.ServerState() != ports.PlatformServerReady || len(upstream.started) != 0 {
				t.Fatal("молчание нескольких магазинов не должно закрыть живой сервер или вызвать лишние повторы")
			}
		})
	}
}

func testSilentCreationServer(
	ctx context.Context,
	t *testing.T,
	load creationLoad,
	clock *fake.Scheduler,
	upstream *scheduledCreationPlatform,
) {
	t.Helper()
	total := 0
	// До открытия размыкателя выпускаем и завершаем целые волны вызовов.
	for range 2 {
		if load.guard.ServerState() != ports.PlatformServerReady {
			break
		}
		before := load.queue.taken()
		load.dispatch(ctx, t)
		taken := load.queue.taken() - before
		if taken == 0 {
			t.Fatal("у живого размыкателя остались невзятые задачи")
		}
		for range taken {
			<-upstream.started
		}
		total += taken
		clock.Advance(appcheckout.CreationCallTimeout)
		for range taken {
			if result := <-load.executor.Results(); !appcheckout.CreationPending(result.Err) {
				t.Fatal("молчание сервера должно оставить повтор", result)
			}
		}
	}
	before := load.queue.calls(appcheckout.TaskCreateOrder)
	load.dispatch(ctx, t)
	if load.guard.ServerState() != ports.PlatformServerBlocked ||
		load.queue.calls(appcheckout.TaskCreateOrder) != before || len(upstream.started) != 0 {
		t.Fatal("размыкатель не остановил взятие и вызовы создания", total)
	}
}

func (load creationLoad) dispatch(ctx context.Context, t *testing.T) {
	t.Helper()
	if err := dispatchForTest(ctx, load.executor); err != nil {
		t.Fatal(err)
	}
}

func (load creationLoad) finishHealthy(ctx context.Context, t *testing.T, expected int) {
	t.Helper()
	for range expected {
		result := <-load.executor.Results()
		if result.Task.Kind != appcheckout.TaskCreateOrder || result.Err != nil {
			t.Fatal("ожидалось успешное создание", result)
		}
		before := load.queue.calls("")
		if _, _, err := load.executor.Refill(ctx, result.Task.Kind); err != nil {
			t.Fatal(err)
		}
		if got := load.queue.calls("") - before; got > 1 {
			t.Fatal("результат создания вызвал больше одного взятия", got)
		}
	}
	if created, expired := load.orderCounts(ctx, t); created != expected || expired != 0 {
		t.Fatal("здоровые магазины не сохранили заказы до логического срока", created, expected, expired)
	}
}

func prepareCreationLoad(
	t *testing.T,
	upstream ports.PlatformAPI,
	clock ports.Clock,
	plans [][]ids.TenantID,
) creationLoad {
	t.Helper()
	store := fake.NewStore()
	price := money.FromKopecks(100)
	offers := make(map[ids.TenantID]ids.OfferID)
	for _, plan := range plans {
		for _, tenant := range plan {
			if _, exists := offers[tenant]; !exists {
				offer := ids.OfferID{Tenant: tenant, Product: "p1"}
				offers[tenant] = offer
				store.SeedBuyerOffer(offer, "Товар", price)
			}
		}
	}
	store.SetDatabaseClock(clock)
	now := clock.Now().Truncate(time.Millisecond)
	deadline := now.Add(appcheckout.CreationTimeout)
	var checkouts []ids.UUID
	err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		for i, plan := range plans {
			id := ids.UUID(fmt.Sprintf("10000000-0000-4000-8000-%012d", i))
			checkouts = append(checkouts, id)
			state := checkout.State{
				ID:             id,
				BuyerID:        buyerID,
				IdempotencyKey: string(id),
				Status:         checkout.StatusCreatingOrders,
				Delivery:       &checkout.Selection{Carrier: "cdek", PickupPointCode: "p1"},
				Contact:        orderContact,
				CartConsumed:   true,
				CreatedAt:      now,
				UpdatedAt:      now,
				Version:        1,
			}
			for _, tenant := range plan {
				state.Groups = append(state.Groups, checkout.Group{
					TenantID:           tenant,
					ExternalCheckoutID: string(id) + ":" + string(tenant),
					Status:             checkout.GroupCreating,
					CreationDeadline:   deadline,
					ExpiresAt:          now.Add(checkout.PaymentWindow),
					Goods:              price,
					Lines: []checkout.Line{
						{Offer: offers[tenant], Quantity: 1, Price: price},
					},
				})
			}
			if err := tx.Checkouts().Add(ctx, checkout.Restore(state)); err != nil {
				return err
			}
			if err := enqueueLoadTasks(ctx, tx, state, now, deadline); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	metrics := &fake.PlatformMetrics{}
	guard := platformguard.New(upstream, executorGuardConfig(), clock, metrics, nil)
	service := appcheckout.NewService(store, guard, nil, nil, &fake.IDs{}, clock, appcheckout.Config{
		OrderCallTimeout: appcheckout.CreationCallTimeout,
	})
	queue := &leaseQueue{TaskQueue: store}
	executor := appcheckout.NewExecutor(queue, clock, service, guard, messaging.DefaultMaxAttempts)
	return creationLoad{
		guard:     guard,
		metrics:   metrics,
		queue:     queue,
		executor:  executor,
		deadline:  deadline,
		checkouts: checkouts,
		store:     store,
	}
}

func enqueueLoadTasks(ctx context.Context, tx ports.Tx, state checkout.State, now, deadline time.Time) error {
	payload := []byte(fmt.Sprintf(`{"BuyerID":%q,"CheckoutID":%q}`, state.BuyerID, state.ID))
	for _, group := range state.Groups {
		for _, kind := range []string{appcheckout.TaskCreateOrder, appcheckout.TaskExpireCreation} {
			task := ports.NewTask{Kind: kind, TenantID: group.TenantID, Payload: payload, At: now}
			if kind == appcheckout.TaskExpireCreation {
				task.AvailableAt = deadline
			}
			if err := tx.Outbox().Enqueue(ctx, task); err != nil {
				return err
			}
		}
	}
	return nil
}

func (load creationLoad) orderCounts(ctx context.Context, t *testing.T) (created, expired int) {
	t.Helper()
	err := load.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for _, id := range load.checkouts {
			purchase, err := tx.Checkouts().Get(ctx, id)
			if err != nil {
				return err
			}
			for _, group := range purchase.Groups() {
				if group.OrderID != "" && group.AwaitingSince.Before(load.deadline) {
					created++
				}
				if group.Status == checkout.GroupCreationExpired {
					expired++
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return created, expired
}

func executorGuardConfig() platformguard.Config {
	return platformguard.Config{
		StrictTimeout:         5 * time.Second,
		ReadTimeout:           300 * time.Millisecond,
		MaxConcurrent:         128,
		TenantMaxConcurrent:   8,
		CheckoutMaxConcurrent: 64,
		ReadMaxConcurrent:     64,
		QueueSize:             1024,
		TenantQueueSize:       32,
		TenantStateSize:       10000,
		TenantIdleTTL:         10 * time.Minute,
		FailureThreshold:      5,
		FailureWindow:         30 * time.Second,
		FailureMinCalls:       20,
		FailurePercent:        50,
		OpenInterval:          5 * time.Second,
	}
}
