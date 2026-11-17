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
	first, allowed := c.acquire(tenantContext("t1"))
	if !allowed {
		t.Fatal("первый вызов не допущен")
	}
	defer c.release(first, nil)
	if _, allowed := c.acquire(tenantContext("t1")); allowed {
		t.Fatal("магазин превысил свою долю")
	}
	other, allowed := c.acquire(tenantContext("t2"))
	if !allowed {
		t.Fatal("медленный магазин занял все места")
	}
	defer c.release(other, nil)
	if _, allowed := c.acquire(tenantContext("t3")); allowed {
		t.Fatal("превышен общий предел")
	}
}

func TestActiveTenantStateCannotBeEvicted(t *testing.T) {
	cfg := testConfig()
	cfg.TenantStateSize = 1
	c := New(fake.NewPlatform(), cfg, fake.NewClock(time.Now()), nil, nil)
	first, allowed := c.acquire(tenantContext("t1"))
	if !allowed {
		t.Fatal("первый вызов не допущен")
	}
	if _, allowed := c.acquire(tenantContext("t2")); allowed {
		t.Fatal("активное состояние вытеснено")
	}
	c.release(first, nil)
	other, allowed := c.acquire(tenantContext("t2"))
	if !allowed {
		t.Fatal("неактивное состояние не вытесняется")
	}
	c.release(other, nil)
}
