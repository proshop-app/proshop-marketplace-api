package platformguard

import (
	"context"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func tenantContext(id ids.TenantID) context.Context {
	return context.WithValue(context.Background(), tenantContextKey{}, id)
}

func admit(t *testing.T, c *Client, tenant ids.TenantID, class operationClass) permit {
	t.Helper()
	result, waiting := c.request(tenantContext(tenant), class)
	if waiting != nil || result.reason != "" {
		t.Fatal("нет немедленного допуска", tenant, result.reason)
	}
	return result.permit
}

func pending(ctx context.Context, t *testing.T, c *Client, class operationClass) *waiter {
	t.Helper()
	result, waiting := c.request(ctx, class)
	if waiting == nil || result.reason != "" {
		t.Fatal("нет ожидающего запроса", result.reason)
	}
	return waiting
}

func TestSlowTenantCannotOccupyAllSlots(t *testing.T) {
	c := New(fake.NewPlatform(), testConfig(), fake.NewClock(time.Now()), nil, nil)
	first := admit(t, c, "t1", checkoutClass)
	waiting := pending(tenantContext("t1"), t, c, checkoutClass)
	other := admit(t, c, "t2", checkoutClass)
	c.release(other, nil)
	select {
	case <-waiting.done:
		t.Fatal("магазин превысил свою долю")
	default:
	}
	c.release(first, nil)
	granted := <-waiting.done
	c.release(granted.permit, nil)
}

func TestActiveTenantStateCannotBeEvicted(t *testing.T) {
	cfg := testConfig()
	cfg.TenantStateSize = 1
	c := New(fake.NewPlatform(), cfg, fake.NewClock(time.Now()), nil, nil)
	first := admit(t, c, "t1", checkoutClass)
	if result, _ := c.request(tenantContext("t2"), checkoutClass); result.reason != "queue_full" {
		t.Fatal("активное состояние вытеснено", result.reason)
	}
	c.release(first, nil)
	other := admit(t, c, "t2", checkoutClass)
	c.release(other, nil)
}

func TestCheckoutPassesWhenReadPoolIsFull(t *testing.T) {
	c := New(fake.NewPlatform(), testConfig(), fake.NewClock(time.Now()), nil, nil)
	first := admit(t, c, "t1", readClass)
	second := admit(t, c, "t2", readClass)
	waiting := pending(tenantContext("t3"), t, c, readClass)
	// Даже у того же магазина чтения не занимают резерв оформления.
	p := admit(t, c, "t1", checkoutClass)
	c.release(p, nil)
	c.release(first, nil)
	c.release((<-waiting.done).permit, nil)
	c.release(second, nil)
}

func TestCallerSelectsPoolForSharedOperations(t *testing.T) {
	for _, operation := range []string{"settings", "price_group", "pickup_points"} {
		for _, class := range []operationClass{checkoutClass, readClass} {
			t.Run(operation+"/"+class.String(), func(t *testing.T) {
				metrics := &fake.PlatformMetrics{}
				c := New(fake.NewPlatform(), testConfig(), fake.NewClock(time.Now()), metrics, nil)
				opposite := readClass
				label := ports.PlatformCheckout
				if class == readClass {
					opposite, label = checkoutClass, ports.PlatformRead
				}
				first := admit(t, c, "t1", opposite)
				second := admit(t, c, "t2", opposite)
				defer c.release(first, nil)
				defer c.release(second, nil)
				ctx := ports.WithPlatformCallClass(context.Background(), label)
				if err := strictCallContext(ctx, c, operation); err != nil {
					t.Fatal("занятый чужой класс задержал вызов", err)
				}
				if metrics.CountClass(operation, class.String(), "ok") != 1 {
					t.Fatal("метрика потеряла класс вызывающего")
				}
			})
		}
	}
}

func TestSlowPurchaseSettingsLeaveReviewSlots(t *testing.T) {
	upstream := fake.NewPlatform()
	upstream.Reviews = []ports.Review{{Author: "Имя", Rating: 5}}
	started, release := make(chan struct{}), make(chan struct{})
	upstream.CallHook = func(ctx context.Context, operation string) error {
		if operation != "settings" {
			return nil
		}
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	cfg := testConfig()
	cfg.StrictTimeout = time.Second
	c := New(upstream, cfg, fake.NewClock(time.Now()), nil, nil)
	ctx := ports.WithPlatformCallClass(context.Background(), ports.PlatformCheckout)
	done := make(chan error, 1)
	go func() {
		_, err := c.MarketplaceSettings(ctx, "t1")
		done <- err
	}()
	<-started
	page, err := c.ProductReviews(ctx, "t1", "p1", 20, 0)
	close(release)
	if settingsErr := <-done; settingsErr != nil {
		t.Fatal(settingsErr)
	}
	if err != nil || len(page.Items) != 1 {
		t.Fatal("настройки покупки заняли долю отзывов магазина", page, err)
	}
}
