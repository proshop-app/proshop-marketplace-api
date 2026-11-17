package platformguard

import (
	"context"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
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
	for _, operation := range []string{"quote", "create_order", "get_order", "stub_payment", "cancel_order"} {
		p := admit(t, c, "t1", classOf(operation))
		c.release(p, nil)
	}
	c.release(first, nil)
	c.release((<-waiting.done).permit, nil)
	c.release(second, nil)
}

func TestOperationClasses(t *testing.T) {
	for _, operation := range []string{"settings", "price_contexts", "price_group", "reviews", "pickup_points"} {
		if classOf(operation) != readClass {
			t.Fatal("чтение попало в оформление", operation)
		}
	}
}
