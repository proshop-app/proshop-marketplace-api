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

func TestOnlyOneProbeAndOtherTenantIsIndependent(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "tenant", true: "server"}[shared], func(t *testing.T) {
			upstream := fake.NewPlatform()
			cfg := testConfig()
			cfg.FailureThreshold = 1
			cfg.StrictTimeout = time.Second
			clock := fake.NewClock(time.Now())
			c := New(upstream, cfg, clock, nil, nil)
			failure := errors.New("сбой магазина")
			if shared {
				failure = &ports.PlatformServerFailure{Cause: failure}
			}
			upstream.Fail("t1", failure)
			_, _ = c.MarketplaceSettings(context.Background(), "t1")
			upstream.Fail("t1", nil)
			clock.Advance(cfg.OpenInterval)
			started, release := make(chan struct{}), make(chan struct{})
			upstream.CallHook = func(ctx context.Context, _ string) error {
				if ctx.Value(tenantContextKey{}) == ids.TenantID("t1") {
					close(started)
					<-release
				}
				return nil
			}
			done := make(chan error, 1)
			go func() {
				_, err := c.MarketplaceSettings(context.Background(), "t1")
				done <- err
			}()
			<-started
			_, ownError := c.MarketplaceSettings(context.Background(), "t1")
			_, otherError := c.MarketplaceSettings(context.Background(), "t2")
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if !errors.Is(ownError, ErrUnavailable) || (otherError != nil) != shared {
				t.Fatal("проба потеряла изоляцию", ownError, otherError)
			}
		})
	}
}

func TestLateSuccessCannotCloseNewBreakerGeneration(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	now := time.Now()
	var b breaker
	failed, late := b.start(), b.start()
	b.finish(failed, verdictFailure, cfg, now)
	b.finish(late, verdictSuccess, cfg, now)
	if b.available(now) {
		t.Fatal("поздний успех закрыл размыкатель")
	}
	now = now.Add(cfg.OpenInterval)
	if !b.available(now) {
		t.Fatal("окно не закончилось")
	}
	probe := b.start()
	if b.available(now) {
		t.Fatal("вторая проба разрешена")
	}
	b.finish(probe, verdictNeutral, cfg, now)
	if !b.available(now) {
		t.Fatal("отмена заблокировала пробу")
	}
	probe = b.start()
	b.finish(probe, verdictSuccess, cfg, now)
	if !b.openUntil.IsZero() || b.failures != 0 {
		t.Fatal("успешная проба не закрыла размыкатель")
	}
}
