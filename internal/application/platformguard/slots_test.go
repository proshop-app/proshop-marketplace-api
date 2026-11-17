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

func TestSlowTenantCannotOccupyAllSlots(t *testing.T) {
	c := New(fake.NewPlatform(), testConfig(), fake.NewClock(time.Now()), nil, nil)
	first, allowed := c.acquire(tenantContext("t1"), checkoutClass)
	if !allowed {
		t.Fatal("первый вызов не допущен")
	}
	defer c.release(first, nil)
	if _, allowed := c.acquire(tenantContext("t1"), checkoutClass); allowed {
		t.Fatal("магазин превысил свою долю")
	}
	other, allowed := c.acquire(tenantContext("t2"), checkoutClass)
	if !allowed {
		t.Fatal("медленный магазин занял все места")
	}
	defer c.release(other, nil)
	if _, allowed := c.acquire(tenantContext("t3"), checkoutClass); allowed {
		t.Fatal("превышен общий предел")
	}
}

func TestActiveTenantStateCannotBeEvicted(t *testing.T) {
	cfg := testConfig()
	cfg.TenantStateSize = 1
	c := New(fake.NewPlatform(), cfg, fake.NewClock(time.Now()), nil, nil)
	first, allowed := c.acquire(tenantContext("t1"), checkoutClass)
	if !allowed {
		t.Fatal("первый вызов не допущен")
	}
	if _, allowed := c.acquire(tenantContext("t2"), checkoutClass); allowed {
		t.Fatal("активное состояние вытеснено")
	}
	c.release(first, nil)
	other, allowed := c.acquire(tenantContext("t2"), checkoutClass)
	if !allowed {
		t.Fatal("неактивное состояние не вытесняется")
	}
	c.release(other, nil)
}

func TestCheckoutPassesWhenReadPoolIsFull(t *testing.T) {
	c := New(fake.NewPlatform(), testConfig(), fake.NewClock(time.Now()), nil, nil)
	first, allowed := c.acquire(tenantContext("t1"), readClass)
	if !allowed {
		t.Fatal("первое чтение не допущено")
	}
	defer c.release(first, nil)
	second, allowed := c.acquire(tenantContext("t2"), readClass)
	if !allowed {
		t.Fatal("второе чтение не допущено")
	}
	defer c.release(second, nil)
	if _, allowed := c.acquire(tenantContext("t3"), readClass); allowed {
		t.Fatal("превышен пул чтений")
	}
	// Даже у того же магазина чтения не занимают резерв оформления.
	for _, operation := range []string{"quote", "create_order", "get_order", "stub_payment", "cancel_order"} {
		p, allowed := c.acquire(tenantContext("t1"), classOf(operation))
		if !allowed {
			t.Fatal("чтения остановили оформление", operation)
		}
		c.release(p, nil)
	}
}

func TestOperationClasses(t *testing.T) {
	for _, operation := range []string{"settings", "price_contexts", "price_group", "reviews", "pickup_points"} {
		if classOf(operation) != readClass {
			t.Fatal("чтение попало в оформление", operation)
		}
	}
}
