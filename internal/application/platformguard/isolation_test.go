package platformguard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestTenantFailuresDoNotCloseOtherShops(t *testing.T) {
	upstream := fake.NewPlatform()
	clock := fake.NewClock(time.Unix(1000, 0))
	cfg := testConfig()
	c := New(upstream, cfg, clock, nil, nil)
	upstream.Fail("t1", errors.New("сбой магазина"))
	for i := 0; i < cfg.FailureThreshold; i++ {
		_, _ = c.MarketplaceSettings(context.Background(), "t1")
		if _, err := c.MarketplaceSettings(context.Background(), "t2"); err != nil {
			t.Fatal("чужая ошибка закрыла магазин", err)
		}
	}
	upstream.Fail("t1", nil)
	if _, err := c.MarketplaceSettings(context.Background(), "t1"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("ошибки магазина не накопились", err)
	}
	clock.Advance(cfg.OpenInterval)
	if _, err := c.MarketplaceSettings(context.Background(), "t1"); err != nil {
		t.Fatal("магазин не восстановился", err)
	}
}

func TestServerFailuresCloseAllShops(t *testing.T) {
	upstream := fake.NewPlatform()
	clock := fake.NewClock(time.Unix(1000, 0))
	cfg := testConfig()
	c := New(upstream, cfg, clock, nil, nil)
	upstream.CallHook = func(context.Context, string) error {
		return &ports.PlatformServerFailure{Cause: errors.New("нет соединения")}
	}
	for i := 0; i < cfg.FailureThreshold; i++ {
		_, _ = c.MarketplaceSettings(context.Background(), "t1")
	}
	upstream.CallHook = nil
	if _, err := c.MarketplaceSettings(context.Background(), "t2"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("общий размыкатель не открылся", err)
	}
	clock.Advance(cfg.OpenInterval)
	if _, err := c.MarketplaceSettings(context.Background(), "t2"); err != nil {
		t.Fatal("сервер не восстановился", err)
	}
	if _, err := c.MarketplaceSettings(context.Background(), "t1"); err != nil {
		t.Fatal("общий сбой ошибочно начислен магазину", err)
	}
}

func TestTenantStatesAreBoundedAndExpire(t *testing.T) {
	cfg := testConfig()
	cfg.TenantStateSize = 2
	clock := fake.NewClock(time.Unix(1000, 0))
	c := New(fake.NewPlatform(), cfg, clock, nil, nil)
	for _, tenant := range []ids.TenantID{"t1", "t2", "t3"} {
		if _, err := c.MarketplaceSettings(context.Background(), tenant); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.tenants) != 2 || c.tenants["t1"] != nil {
		t.Fatal("состояния не ограничены", len(c.tenants))
	}
	previous := c.tenants["t2"]
	clock.Advance(cfg.TenantIdleTTL)
	_, _ = c.MarketplaceSettings(context.Background(), "t2")
	if c.tenants["t2"] == previous {
		t.Fatal("неактивное состояние не истекло")
	}
}
