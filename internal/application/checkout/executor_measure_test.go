package checkout_test

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const loadReplyDelay = 50 * time.Millisecond

type executorWallClock struct{}

type creationLoadPlatform struct {
	*fake.Platform
	silent      map[ids.TenantID]bool
	replyDelay  time.Duration
	quoteStarts chan struct{}
	silentCalls atomic.Int32
}

type creationLoadResult struct {
	created int
	expired int
	results int
	refills int
	claims  int
}

func TestMeasureExecutorCreationWithConcurrentDeliveryQuotes(t *testing.T) {
	requireCreationMeasurement(t)
	for _, shoppers := range []int{3, 6} {
		t.Run(fmt.Sprint(shoppers), func(t *testing.T) {
			const purchases = 200
			plans := make([][]ids.TenantID, purchases)
			for i := range plans {
				plans[i] = []ids.TenantID{"t1", "t2"}
			}
			upstream := &creationLoadPlatform{
				Platform:    fake.NewPlatform(),
				replyDelay:  loadReplyDelay,
				quoteStarts: make(chan struct{}, shoppers),
			}
			upstream.OrderClock = executorWallClock{}
			load := prepareCreationLoad(t, upstream, executorWallClock{}, plans)
			ctx, cancel := context.WithCancel(context.Background())
			var buyers sync.WaitGroup
			quoteErrors := make(chan error, shoppers)
			defer func() {
				cancel()
				buyers.Wait()
				load.executor.Wait()
			}()
			for range shoppers {
				buyers.Add(1)
				go func() {
					defer buyers.Done()
					for ctx.Err() == nil {
						_, err := load.guard.QuoteDelivery(ctx, "t1", ports.QuoteRequest{})
						if err != nil && ctx.Err() == nil {
							quoteErrors <- err
							return
						}
					}
				}()
			}
			for range min(4, shoppers) {
				select {
				case <-upstream.quoteStarts:
				case <-time.After(time.Second):
					t.Fatal("покупатели не начали расчёт доставки")
				}
			}
			stats := load.measure(ctx, t, upstream.silent, 2*purchases)
			t.Logf(
				"создано %d/%d, истекло %d, взятий %d/%d результатов (на пополнение %d)",
				stats.created,
				2*purchases,
				stats.expired,
				stats.claims,
				stats.results,
				stats.refills,
			)
			select {
			case err := <-quoteErrors:
				t.Fatal(err)
			default:
			}
		})
	}
}

func TestMeasureExecutorCreationWithSilentShops(t *testing.T) {
	requireCreationMeasurement(t)
	for _, tc := range []struct {
		shops  int
		silent int
	}{
		{shops: 500, silent: 25},
		{shops: 100, silent: 50},
		{shops: 200, silent: 200},
	} {
		name := fmt.Sprintf("%d_из_%d", tc.silent, tc.shops)
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			upstream := &creationLoadPlatform{
				Platform:   fake.NewPlatform(),
				replyDelay: 10 * time.Millisecond,
				silent:     map[ids.TenantID]bool{},
			}
			plans := make([][]ids.TenantID, 0, 2*tc.shops)
			for i := range tc.shops {
				tenant := ids.TenantID(fmt.Sprintf("t%03d", i))
				if i < tc.silent {
					upstream.silent[tenant] = true
				}
				plans = append(plans, []ids.TenantID{tenant}, []ids.TenantID{tenant})
			}
			if tc.silent == tc.shops {
				upstream.CallHook = func(ctx context.Context, _ string) error {
					<-ctx.Done()
					return ctx.Err()
				}
			}
			upstream.OrderClock = executorWallClock{}
			load := prepareCreationLoad(t, upstream, executorWallClock{}, plans)
			defer func() {
				cancel()
				load.executor.Wait()
			}()
			expected := 2 * (tc.shops - tc.silent)
			stats := load.measure(ctx, t, upstream.silent, max(1, expected))
			calls := int(upstream.silentCalls.Load())
			t.Logf("здоровых заказов %d/%d, вызовов к молчащим %d", stats.created, expected, calls)
			t.Log("размыкатель сервера:", load.guard.ServerState())

		})
	}
}

func requireCreationMeasurement(t *testing.T) {
	t.Helper()
	if os.Getenv("ORDER_EXECUTOR_MEASURE") != "1" {
		t.Skip("замер на реальных часах: ORDER_EXECUTOR_MEASURE=1")
	}
}

func (load creationLoad) measure(
	ctx context.Context,
	t *testing.T,
	silent map[ids.TenantID]bool,
	expected int,
) creationLoadResult {
	t.Helper()
	var stats creationLoadResult
	var latencies []time.Duration
	started := time.Now()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	stop := time.NewTimer(time.Until(load.deadline.Add(500 * time.Millisecond)))
	defer stop.Stop()
	if err := dispatchForTest(ctx, load.executor); err != nil {
		t.Fatal(err)
	}
	finished := 0
measure:
	for {
		select {
		case <-ticker.C:
			if err := dispatchForTest(ctx, load.executor); err != nil {
				t.Fatal(err)
			}
		case result := <-load.executor.Results():
			stats.results++
			before := load.queue.calls("")
			if _, _, err := load.executor.Refill(ctx, result.Task.Kind); err != nil {
				t.Fatal(err)
			}
			refills := load.queue.calls("") - before
			stats.refills += refills
			if result.Task.Kind == appcheckout.TaskCreateOrder && result.Err == nil && !silent[result.Task.TenantID] {
				finished++
				latencies = append(latencies, result.Lag)
				if finished == expected {
					break measure
				}
			}
		case <-stop.C:
			break measure
		}
	}
	stats.claims = load.queue.calls("")
	stats.created, stats.expired = load.orderCounts(ctx, t)
	elapsed := time.Since(started)
	var p50 time.Duration
	if len(latencies) > 0 {
		slices.Sort(latencies)
		p50 = latencies[(len(latencies)-1)/2]
	}
	perSecond := float64(stats.created) / elapsed.Seconds()
	t.Logf("p50 от постановки в очередь: %s, созданий/с: %.1f за %s", p50, perSecond, elapsed)
	return stats
}

func (executorWallClock) Now() time.Time { return time.Now() }

func (executorWallClock) Wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (p *creationLoadPlatform) CreateOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	if p.silent[tenant] {
		p.silentCalls.Add(1)
		<-ctx.Done()
		return checkout.SellerOrder{}, ctx.Err()
	}
	if !(executorWallClock{}).Wait(ctx, p.replyDelay) {
		return checkout.SellerOrder{}, ctx.Err()
	}
	return p.Platform.CreateOrder(ctx, tenant, request)
}

func (p *creationLoadPlatform) QuoteDelivery(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.QuoteRequest,
) (delivery.Offered, error) {
	select {
	case p.quoteStarts <- struct{}{}:
	default:
	}
	if !(executorWallClock{}).Wait(ctx, p.replyDelay) {
		return delivery.Offered{}, ctx.Err()
	}
	return p.Platform.QuoteDelivery(ctx, tenant, request)
}
