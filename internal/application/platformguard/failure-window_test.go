package platformguard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestAlternatingFailuresOpenEachScopeIndependently(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "tenant", true: "server"}[shared], func(t *testing.T) {
			cfg := testConfig()
			cfg.FailureThreshold = 100
			upstream := fake.NewPlatform()
			c := New(upstream, cfg, fake.NewClock(time.Now()), nil, nil)
			for i := 0; i < cfg.FailureMinCalls; i++ {
				var failure error
				if i%2 == 0 {
					failure = errors.New("сбой магазина")
					if shared {
						failure = &ports.PlatformServerFailure{Cause: failure}
					}
				}
				upstream.Fail("t1", failure)
				_, _ = c.MarketplaceSettings(context.Background(), "t1")
			}
			upstream.Fail("t1", nil)
			if _, err := c.MarketplaceSettings(context.Background(), "t1"); !errors.Is(err, ErrUnavailable) {
				t.Fatal("половина ошибок не открыла размыкатель", err)
			}
			_, otherError := c.MarketplaceSettings(context.Background(), "t2")
			if (otherError != nil) != shared {
				t.Fatal("окна магазина и сервера смешались", otherError)
			}
		})
	}
}

func TestRareFailuresAndSmallSampleDoNotOpenBreaker(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 100
	now := time.Now()
	var rare, small breaker
	for i := 0; i < 1000; i++ {
		v := verdictSuccess
		if i%100 == 0 {
			v = verdictFailure
		}
		rare.finish(rare.start(), v, cfg, now)
	}
	for i := 0; i < cfg.FailureMinCalls-1; i++ {
		small.finish(small.start(), verdictFailure, cfg, now)
	}
	if !rare.available(now) || !small.available(now) {
		t.Fatal("редкий отказ или малая выборка открыли размыкатель")
	}
}

func TestWindowExpiresBucketsAndLongIdlePeriod(t *testing.T) {
	cfg := testConfig()
	cfg.FailureWindow = 32 * time.Second
	cfg.FailureMinCalls = 10
	cfg.FailureThreshold = 100
	now := time.Unix(1000, 0)
	var b breaker
	for i := 0; i < 4; i++ {
		b.finish(b.start(), verdictFailure, cfg, now)
	}
	now = now.Add(31 * time.Second)
	for i := 0; i < 4; i++ {
		b.finish(b.start(), verdictSuccess, cfg, now)
	}
	now = now.Add(time.Second)
	for i := 0; i < 10; i++ {
		b.finish(b.start(), verdictSuccess, cfg, now)
	}
	if !b.available(now) || b.window.failures != 0 || b.window.calls != 14 {
		t.Fatal("старая корзина не забыта", b.window.calls, b.window.failures)
	}
	now = now.Add(cfg.FailureWindow)
	b.finish(b.start(), verdictSuccess, cfg, now)
	if b.window.calls != 1 || b.window.failures != 0 {
		t.Fatal("простой не очистил окно")
	}
}

func TestNeutralResultsAndProbeDoNotPolluteWindow(t *testing.T) {
	cfg := testConfig()
	now := time.Now()
	var b breaker
	for i := 0; i < 100; i++ {
		b.finish(b.start(), verdictNeutral, cfg, now)
	}
	if b.window.calls != 0 || b.failures != 0 {
		t.Fatal("нейтральные исходы попали в долю")
	}
	b.open(cfg.OpenInterval, now)
	now = now.Add(cfg.OpenInterval)
	b.finish(b.start(), verdictSuccess, cfg, now)
	if !b.available(now) || b.window.calls != 0 {
		t.Fatal("проба не начала новое окно")
	}
}

func TestCallerDeadlineIsNeutralForBothBreakers(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	cfg.FailureMinCalls = 1
	upstream := fake.NewPlatform()
	upstream.CallHook = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	c := New(upstream, cfg, fake.NewClock(time.Now()), nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := c.MarketplaceSettings(ctx, "t1"); !errors.Is(err, ErrTimeout) {
		t.Fatal(err)
	}
	if c.server.window.calls != 0 || c.tenants["t1"].breaker.window.calls != 0 {
		t.Fatal("бюджет вызывающего кода начислен серверу или магазину")
	}
	upstream.CallHook = nil
	if _, err := c.MarketplaceSettings(context.Background(), "t1"); err != nil {
		t.Fatal("таймаут клиента открыл размыкатель", err)
	}
}
