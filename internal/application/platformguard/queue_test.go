package platformguard

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestWaitingAndCancellationReleaseQueuePlace(t *testing.T) {
	cfg := testConfig()
	cfg.QueueSize = 1
	cfg.TenantQueueSize = 1
	metrics := &fake.PlatformMetrics{}
	c := New(fake.NewPlatform(), cfg, guardTestClock(), metrics, nil)
	first := admit(t, c, "t1", checkoutClass)
	second := admit(t, c, "t2", checkoutClass)
	ctx, cancel := context.WithCancel(tenantContext("t3"))
	waiting := pending(ctx, t, c, checkoutClass)
	if result, _ := c.request(tenantContext("t4"), checkoutClass); result.reason != "queue_full" {
		t.Fatal("очередь превысила предел", result.reason)
	}
	cancel()
	if result := c.await(ctx, waiting); result.reason != "canceled" {
		t.Fatal(result.reason)
	}
	next := pending(tenantContext("t4"), t, c, checkoutClass)
	c.release(first, nil)
	granted := c.await(tenantContext("t4"), next)
	if granted.reason != "" {
		t.Fatal(granted.reason)
	}
	c.release(granted.permit, nil)
	c.release(second, nil)
	if metrics.QueueCount("checkout", "canceled") != 1 || metrics.QueueCount("checkout", "admitted") != 1 {
		t.Fatal("нет метрик ожидания")
	}
	if c.running != 0 || c.pools[checkoutClass].queued != 0 {
		t.Fatal("место не освобождено")
	}
}

func TestTenantQueueCannotFillPoolOrBlockReadyShop(t *testing.T) {
	cfg := testConfig()
	cfg.TenantQueueSize = 1
	c := New(fake.NewPlatform(), cfg, guardTestClock(), nil, nil)
	first := admit(t, c, "t1", checkoutClass)
	second := admit(t, c, "t2", checkoutClass)
	noisy := pending(tenantContext("t1"), t, c, checkoutClass)
	if result, _ := c.request(tenantContext("t1"), checkoutClass); result.reason != "queue_full" {
		t.Fatal("магазин заполнил очередь", result.reason)
	}
	other := pending(tenantContext("t3"), t, c, checkoutClass)
	c.release(second, nil)
	granted := <-other.done
	select {
	case <-noisy.done:
		t.Fatal("магазин превысил долю")
	default:
	}
	c.release(granted.permit, nil)
	c.release(first, nil)
	c.release((<-noisy.done).permit, nil)
}

func TestQueueDeadlineUsesOperationBudgetWithoutCallingServer(t *testing.T) {
	upstream := fake.NewPlatform()
	var calls atomic.Int32
	upstream.CallHook = func(context.Context, string) error {
		calls.Add(1)
		return nil
	}
	metrics := &fake.PlatformMetrics{}
	clock := guardTestClock()
	c := New(upstream, testConfig(), clock, metrics, nil)
	first := admit(t, c, "t1", readClass)
	second := admit(t, c, "t2", readClass)
	for _, budget := range []time.Duration{time.Millisecond, time.Second} {
		ctx, cancel := clock.WithTimeout(tenantContext("t3"), budget)
		done := make(chan error, 1)
		go func() {
			_, err := c.MarketplaceSettings(ctx, "t3")
			done <- err
		}()
		awaitQueued(c, readClass)
		clock.Advance(min(budget, c.cfg.StrictTimeout))
		err := <-done
		cancel()
		if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	}
	c.release(first, nil)
	c.release(second, nil)
	if calls.Load() != 0 || metrics.Count("settings", "queue_timeout") != 2 ||
		metrics.QueueCount("read", "queue_timeout") != 2 {
		t.Fatal("ожидание вызвало сервер или потеряло метрику", calls.Load())
	}
	if c.pools[readClass].queued != 0 || c.tenants["t3"].breaker.failures != 0 {
		t.Fatal("таймаут ожидания оставил место или ошибку магазина")
	}
}

func TestCancellationRacingWithDispatchDoesNotLeakSlots(t *testing.T) {
	c := New(fake.NewPlatform(), testConfig(), guardTestClock(), nil, nil)
	for i := 0; i < 100; i++ {
		first := admit(t, c, "t1", checkoutClass)
		ctx, cancel := context.WithCancel(tenantContext("t1"))
		waiting := pending(ctx, t, c, checkoutClass)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			cancel()
		}()
		go func() {
			defer wg.Done()
			c.release(first, nil)
		}()
		result := c.await(ctx, waiting)
		if result.reason == "" {
			c.release(result.permit, context.Canceled)
		}
		wg.Wait()
		if c.running != 0 || c.pools[checkoutClass].queued != 0 || c.idle.Len() != 1 {
			t.Fatal("утечка места после отмены", i)
		}
	}
}

func TestOpeningTenantBreakerRejectsOnlyItsQueue(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	c := New(fake.NewPlatform(), cfg, guardTestClock(), nil, nil)
	first := admit(t, c, "t1", checkoutClass)
	second := admit(t, c, "t2", checkoutClass)
	own := pending(tenantContext("t1"), t, c, checkoutClass)
	other := pending(tenantContext("t3"), t, c, checkoutClass)
	c.release(first, errors.New("сбой магазина"))
	if result := <-own.done; result.reason != "tenant_breaker" {
		t.Fatal(result.reason)
	}
	granted := <-other.done
	if granted.reason != "" {
		t.Fatal("сбой магазина отклонил чужой запрос", granted.reason)
	}
	c.release(granted.permit, nil)
	c.release(second, nil)
}

func TestServerBreakerReleasesQueuesOfBothClasses(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	c := New(fake.NewPlatform(), cfg, guardTestClock(), nil, nil)
	checkout := admit(t, c, "t1", checkoutClass)
	read := admit(t, c, "t2", readClass)
	ownCheckout := pending(tenantContext("t1"), t, c, checkoutClass)
	ownRead := pending(tenantContext("t2"), t, c, readClass)
	failure := &ports.PlatformServerFailure{Cause: errors.New("нет соединения")}
	c.release(checkout, failure)
	for _, waiting := range []*waiter{ownCheckout, ownRead} {
		if result := c.await(waiting.ctx, waiting); result.reason != "server_breaker" {
			t.Fatal("очередь не освобождена при общем отказе", result.reason)
		}
	}
	c.release(read, context.Canceled)
	if c.pools[checkoutClass].queued != 0 || c.pools[readClass].queued != 0 || c.running != 0 {
		t.Fatal("общий отказ оставил занятые места")
	}
}

// awaitQueued синхронизирует сдвиг часов с постановкой запроса в очередь.
func awaitQueued(c *Client, class operationClass) {
	for {
		c.mu.Lock()
		queued := c.pools[class].queued
		c.mu.Unlock()
		if queued != 0 {
			return
		}
		runtime.Gosched()
	}
}
