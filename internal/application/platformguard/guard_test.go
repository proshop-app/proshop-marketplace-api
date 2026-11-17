package platformguard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func testConfig() Config {
	return Config{
		ReadTimeout:           10 * time.Millisecond,
		StrictTimeout:         20 * time.Millisecond,
		CacheTTL:              time.Second,
		StaleTTL:              time.Minute,
		CacheSize:             3,
		TenantMaxConcurrent:   2,
		CheckoutMaxConcurrent: 2,
		ReadMaxConcurrent:     2,
		QueueSize:             8,
		TenantQueueSize:       2,
		TenantStateSize:       100,
		TenantIdleTTL:         time.Minute,
		MaxConcurrent:         4,
		FailureThreshold:      2,
		OpenInterval:          time.Second,
		LogInterval:           time.Minute,
	}
}

func TestBreakerRecoveryAndLogLimit(t *testing.T) {
	clock := fake.NewClock(time.Now())
	upstream := fake.NewPlatform()
	metrics := &fake.PlatformMetrics{}
	var logs bytes.Buffer
	c := New(upstream, testConfig(), clock, metrics, slog.New(slog.NewJSONHandler(&logs, nil)))
	upstream.Fail("t1", errors.New("нет связи"))
	for i := 0; i < 4; i++ {
		_, err := c.MarketplaceSettings(context.Background(), "t1")
		if !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
	}
	if metrics.Count("settings", "error") != 2 || metrics.Count("settings", "tenant_breaker") != 2 {
		t.Fatal("предохранитель не открылся")
	}
	if strings.Count(logs.String(), "\n") != 2 {
		t.Fatal(logs.String())
	}
	clock.Advance(time.Second)
	// Неудачная проба снова открывает предохранитель.
	_, _ = c.MarketplaceSettings(context.Background(), "t1")
	_, _ = c.MarketplaceSettings(context.Background(), "t1")
	if metrics.Count("settings", "error") != 3 || metrics.Count("settings", "tenant_breaker") != 3 {
		t.Fatal("неудачная проба не открыла предохранитель")
	}
	upstream.Fail("t1", nil)
	clock.Advance(time.Second)
	for i := 0; i < 2; i++ {
		if _, err := c.MarketplaceSettings(context.Background(), "t1"); err != nil {
			t.Fatal(err)
		}
	}
	if metrics.Count("settings", "ok") != 2 {
		t.Fatal("предохранитель не закрылся")
	}
	upstream.Fail("t1", errors.New("нет связи"))
	clock.Advance(time.Minute)
	_, _ = c.MarketplaceSettings(context.Background(), "t1")
	if strings.Count(logs.String(), "\n") != 3 {
		t.Fatal("журнал не возобновился", logs.String())
	}
}

func TestStrictTimeoutsAndCodes(t *testing.T) {
	operations := []string{
		"price_group",
		"settings",
		"quote",
		"create_order",
		"get_order",
		"stub_payment",
		"cancel_order",
		"pickup_points",
	}
	for _, operation := range operations {
		t.Run(operation, func(t *testing.T) {
			upstream := fake.NewPlatform()
			upstream.CallHook = func(ctx context.Context, _ string) error {
				<-ctx.Done()
				return ctx.Err()
			}
			upstream.BeforePickup = func(ctx context.Context, _ ids.TenantID) error {
				<-ctx.Done()
				return ctx.Err()
			}
			metrics := &fake.PlatformMetrics{}
			c := New(upstream, testConfig(), fake.NewClock(time.Now()), metrics, nil)
			err := strictCall(c, operation)
			if !errors.Is(err, ErrTimeout) || errs.CodeOf(err) != errs.Unavailable {
				t.Fatal(err)
			}
			if metrics.Count(operation, "timeout") != 1 {
				t.Fatal("нет метрики таймаута", operation)
			}
		})
	}
}

func strictCall(c *Client, operation string) error {
	ctx := context.Background()
	switch operation {
	case "price_group":
		_, err := c.PriceGroup(ctx, "t1", ports.GroupPricingRequest{})
		return err
	case "settings":
		_, err := c.MarketplaceSettings(ctx, "t1")
		return err
	case "quote":
		_, err := c.QuoteDelivery(ctx, "t1", ports.QuoteRequest{})
		return err
	case "create_order":
		_, err := c.CreateOrder(ctx, "t1", ports.CreateOrderRequest{})
		return err
	case "get_order":
		_, err := c.GetOrder(ctx, "t1", "o1")
		return err
	case "cancel_order":
		_, err := c.CancelOrder(ctx, "t1", ports.CancelOrderRequest{})
		return err
	case "pickup_points":
		_, err := c.PickupPoints(ctx, "t1", "carrier", "Город")
		return err
	default:
		return c.StubPayment(ctx, "t1", "o1", money.Money{})
	}
}

func TestContextLoadNeverRenewsStaleValue(t *testing.T) {
	ctx := context.Background()
	upstream := fake.NewPlatform()
	c := New(upstream, testConfig(), fake.NewClock(time.Now()), nil, nil)
	upstream.SetBuyerProfile("b1", "t1", "marketplace:buyer")
	got, err := c.PriceContexts(ctx, "b1", []ids.TenantID{"t1"})
	if err != nil || got["t1"] != "marketplace:buyer" {
		t.Fatal(got, err)
	}
	upstream.Fail("t1", errors.New("недоступен"))
	got, err = c.PriceContexts(ctx, "b1", []ids.TenantID{"t1"})
	if err == nil || len(got) != 0 {
		t.Fatal("устаревший контекст выдан как подтверждённая новая загрузка", got, err)
	}
}

func TestReviewsStaleAndColdTimeout(t *testing.T) {
	ctx := context.Background()
	upstream := fake.NewPlatform()
	clock := fake.NewClock(time.Now())
	metrics := &fake.PlatformMetrics{}
	upstream.Reviews = []ports.Review{{Author: "Имя", Rating: 5}}
	c := New(upstream, testConfig(), clock, metrics, nil)
	page, _ := c.ProductReviews(ctx, "t1", "p1", 20, 0)
	page.Items[0].Author = "испорчено"
	upstream.CallHook = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	clock.Advance(2 * time.Second)
	page, err := c.ProductReviews(ctx, "t1", "p1", 20, 0)
	if err != nil || page.Items[0].Author != "Имя" || metrics.Count("reviews", "stale") != 1 {
		t.Fatal(page, err)
	}
	page, err = c.ProductReviews(ctx, "t1", "p2", 20, 0)
	if err != nil || page.Items == nil || len(page.Items) != 0 || metrics.Count("reviews", "timeout") != 2 {
		t.Fatal(page, err)
	}
}

func TestBusinessRefusalsDoNotOpenBreaker(t *testing.T) {
	upstream := fake.NewPlatform()
	cfg := testConfig()
	cfg.FailureThreshold = 1
	c := New(upstream, cfg, fake.NewClock(time.Now()), nil, nil)
	for _, refusal := range []error{ports.ErrMarketplaceDisabled, &ports.OrderRefusal{Code: "OUT_OF_STOCK"}} {
		upstream.CallHook = func(context.Context, string) error { return refusal }
		_, err := c.CreateOrder(context.Background(), "t1", ports.CreateOrderRequest{})
		if !errors.Is(err, refusal) {
			t.Fatal(err)
		}
	}
	upstream.CallHook = nil
	if _, err := c.MarketplaceSettings(context.Background(), "t2"); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentCacheLoadsDoNotWaitAndRemainBounded(t *testing.T) {
	upstream := fake.NewPlatform()
	cfg := testConfig()
	cfg.ReadTimeout = time.Second
	metrics := &fake.PlatformMetrics{}
	c := New(upstream, cfg, fake.NewClock(time.Now()), metrics, nil)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	upstream.CallHook = func(ctx context.Context, _ string) error {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	first := make(chan ports.ReviewPage, 1)
	go func() {
		value, _ := c.ProductReviews(context.Background(), "t1", "p1", 20, 0)
		first <- value
	}()
	<-started
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := c.ProductReviews(context.Background(), "t1", "p1", 20, 0)
			if err != nil || len(got.Items) != 0 {
				t.Error(got, err)
			}
		}()
	}
	wg.Wait()
	close(release)
	if got := <-first; got.Items == nil {
		t.Fatal(got)
	}
	if calls.Load() != 1 || metrics.Count("reviews", "short_circuit") != 20 {
		t.Fatal("общая загрузка не ограничила обращения", calls.Load())
	}
	upstream.CallHook = nil
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = c.ProductReviews(context.Background(), "t1", ids.ProductID(fmt.Sprint(i)), 20, 0)
			c.mu.Lock()
			defer c.mu.Unlock()
			if len(c.cache) > cfg.CacheSize {
				t.Error("кэш превысил предел", len(c.cache))
			}
		}(i)
	}
	wg.Wait()
}

func TestCancelledCallDoesNotOpenBreaker(t *testing.T) {
	upstream := fake.NewPlatform()
	cfg := testConfig()
	cfg.FailureThreshold = 1
	c := New(upstream, cfg, fake.NewClock(time.Now()), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = c.MarketplaceSettings(ctx, "t1")
	if _, err := c.MarketplaceSettings(context.Background(), "t1"); err != nil {
		t.Fatal("отмена клиента открыла предохранитель", err)
	}
}
