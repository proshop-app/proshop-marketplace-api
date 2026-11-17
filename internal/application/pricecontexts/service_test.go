package pricecontexts_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/platformguard"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/pricecontexts"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const buyerID ids.UUID = "00000000-0000-4000-8000-000000000001"

type fixture struct {
	service  *pricecontexts.Service
	cache    *fake.PriceContextCache
	platform *fake.Platform
	store    *fake.Store
	metrics  *fake.PriceContextMetrics
	clock    *fake.Clock
}

func setup(t *testing.T, cfg pricecontexts.Config) *fixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f := &fixture{
		cache:    &fake.PriceContextCache{},
		platform: fake.NewPlatform(),
		store:    fake.NewStore(),
		metrics:  &fake.PriceContextMetrics{Events: make(chan string, 256)},
		clock:    fake.NewClock(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
	}
	f.store.SeedTenant(participant.TenantState{TenantID: "t1", Status: participant.TenantActive})
	f.store.SeedParticipant(participant.State{
		TenantID: "t1",
		Enabled:  true,
		Stage:    participant.StageConnected,
		Subsidy:  participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
		Delivery: participant.DeliverySettings{Regions: []string{"RU-MOW"}, Carriers: []string{"cdek"}},
	})
	f.platform.SetBuyerProfile(buyerID, "t1", "marketplace:buyer")
	guard := platformguard.New(
		f.platform,
		platformguard.Config{
			ReadTimeout:           200 * time.Millisecond,
			TenantMaxConcurrent:   2,
			CheckoutMaxConcurrent: 2,
			ReadMaxConcurrent:     2,
			TenantStateSize:       100,
			TenantIdleTTL:         time.Minute,
			MaxConcurrent:         4,
			FailureThreshold:      2,
			OpenInterval:          time.Second,
			LogInterval:           time.Minute,
		},
		f.clock,
		&fake.PlatformMetrics{},
		nil,
	)
	f.service = pricecontexts.New(ctx, cfg, f.store, guard, f.cache, f.clock, &fake.IDs{}, f.metrics)
	t.Cleanup(f.service.Stop)
	return f
}

func config() pricecontexts.Config {
	return pricecontexts.Config{TTL: time.Minute, LoadTimeout: time.Second, MaxConcurrent: 2}
}

func wait(t *testing.T, f *fixture, outcome string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-f.metrics.Events:
			if event == "load:"+outcome {
				return
			}
		case <-deadline:
			t.Fatal("фоновая загрузка не завершилась", outcome)
		}
	}
}

func seed(t *testing.T, f *fixture, loaded time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := f.cache.Begin(ctx, buyerID, "seed", time.Minute); err != nil {
		t.Fatal(err)
	}
	_, err := f.cache.Store(
		ctx,
		buyerID,
		"seed",
		ports.PriceContextEntry{
			LoadedAt: loaded,
			Profiles: map[ids.TenantID]string{"t1": "marketplace:buyer"},
		},
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHitDoesNotCallServer(t *testing.T) {
	f := setup(t, config())
	seed(t, f, f.clock.Now())
	f.platform.CallHook = func(context.Context, string) error {
		t.Error("попадание вызвало сервер")
		return nil
	}
	got, err := f.service.PriceContexts(context.Background(), buyerID, []ids.TenantID{"t1"})
	if err != nil || got["t1"] != "marketplace:buyer" || f.metrics.Count("cache", "hit") != 1 {
		t.Fatal(got, err)
	}
}

func TestMissReturnsPublicBeforeLoadFinishesAndCoalesces(t *testing.T) {
	f := setup(t, config())
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f.platform.CallHook = func(ctx context.Context, _ string) error {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx := context.Background()
	got, err := f.service.PriceContexts(ctx, buyerID, []ids.TenantID{"t1"})
	if err != nil || got["t1"] != catalog.DefaultPriceProfile {
		t.Fatal(got, err)
	}
	waitStarted(t, started)
	for range 20 {
		f.service.Preload(buyerID)
	}
	close(release)
	wait(t, f, "ok")
	got, err = f.service.PriceContexts(ctx, buyerID, []ids.TenantID{"t1"})
	if err != nil || got["t1"] != "marketplace:buyer" || calls.Load() != 1 {
		t.Fatal(got, err, calls.Load())
	}
}

func TestFailureAndTimeoutRemainPublic(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "timeout"}[timeout], func(t *testing.T) {
			f := setup(t, config())
			f.platform.CallHook = func(ctx context.Context, _ string) error {
				if timeout {
					<-ctx.Done()
					return ctx.Err()
				}
				return errs.New(errs.Unavailable, "test.unavailable", "отказ")
			}
			got, err := f.service.PriceContexts(context.Background(), buyerID, []ids.TenantID{"t1"})
			if err != nil || got["t1"] != catalog.DefaultPriceProfile {
				t.Fatal(got, err)
			}
			// Guard переводит собственный таймаут в типизированную ошибку; запись не сохраняется.
			wait(t, f, map[bool]string{false: "error", true: "timeout"}[timeout])
			_, found, err := f.cache.Get(context.Background(), buyerID)
			if err != nil || found {
				t.Fatal("отказ записан как подтверждённый контекст", found, err)
			}
		})
	}
}

func TestExpiredAndUnavailableCacheNeverUsePersonal(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		f := setup(t, config())
		seed(t, f, f.clock.Now().Add(-time.Minute))
		if unavailable {
			f.cache.GetError = errs.New(errs.Unavailable, "cache.unavailable", "отказ")
		}
		got, err := f.service.PriceContexts(context.Background(), buyerID, []ids.TenantID{"t1"})
		if err != nil || got["t1"] != catalog.DefaultPriceProfile {
			t.Fatal(got, err)
		}
		wait(t, f, "ok")
	}
}

func TestInvalidationCancelsInFlightWriteAndDeduplicates(t *testing.T) {
	f := setup(t, config())
	started, release := make(chan struct{}), make(chan struct{})
	f.platform.CallHook = func(ctx context.Context, _ string) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.service.Preload(buyerID)
	waitStarted(t, started)
	event := pricecontexts.Changed{EventID: "topic:0:1", TenantID: "t1", BuyerID: buyerID}
	if err := f.service.Invalidate(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	close(release)
	wait(t, f, "invalidated")
	if err := f.service.Invalidate(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	_, found, _ := f.cache.Get(context.Background(), buyerID)
	if found || f.metrics.Count("event", "duplicate") != 1 || f.metrics.Count("event", "unknown_buyer") != 1 {
		t.Fatal("сброс потерян или повторился")
	}
}

func TestParallelismIsBounded(t *testing.T) {
	cfg := config()
	cfg.MaxConcurrent = 1
	f := setup(t, cfg)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f.platform.CallHook = func(ctx context.Context, _ string) error {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.service.Preload(buyerID)
	waitStarted(t, started)
	f.service.Preload("b2")
	close(release)
	wait(t, f, "ok")
	if calls.Load() != 1 || f.metrics.Count("load", "skipped") != 1 {
		t.Fatal(calls.Load())
	}
}

func waitStarted(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("сервер не получил фоновый запрос")
	}
}

func TestEventFailureRollsBackInboxAndCanBeRetried(t *testing.T) {
	f := setup(t, config())
	ctx := context.Background()
	err := f.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Buyers().Add(ctx, buyer.New(buyerID, "79991234567", f.clock.Now()))
	})
	if err != nil {
		t.Fatal(err)
	}

	seed(t, f, f.clock.Now())
	f.cache.DeleteError = errs.New(errs.Unavailable, "cache.failed", "отказ кэша")
	event := pricecontexts.Changed{EventID: "topic:0:2", TenantID: "t1", BuyerID: buyerID}
	if err := f.service.Invalidate(ctx, event); err == nil || f.store.Processed(event.EventID) {
		t.Fatal("ошибка сброса отмечена в inbox", err)
	}
	f.cache.DeleteError = nil
	if err := f.service.Invalidate(ctx, event); err != nil || !f.store.Processed(event.EventID) {
		t.Fatal("повтор сброса не завершился", err)
	}
	if _, found, _ := f.cache.Get(ctx, buyerID); found {
		t.Fatal("после события остался персональный контекст")
	}
	if f.metrics.Count("event", "invalidated") != 1 {
		t.Fatal("событие известного покупателя не учтено")
	}

}

func TestAllowedStaleIsBoundedAndNeverRenewedByFailure(t *testing.T) {
	cfg := config()
	cfg.TTL = 45 * time.Second
	cfg.StaleTTL = 15 * time.Second
	f := setup(t, cfg)
	seed(t, f, f.clock.Now().Add(-50*time.Second))
	f.platform.Fail("t1", errs.New(errs.Unavailable, "test.failed", "отказ"))
	got, _ := f.service.PriceContexts(context.Background(), buyerID, []ids.TenantID{"t1"})
	if got["t1"] != "marketplace:buyer" {
		t.Fatal("допустимый stale не использован", got)
	}
	wait(t, f, "error")
	f.clock.Advance(10 * time.Second)
	got, _ = f.service.PriceContexts(context.Background(), buyerID, []ids.TenantID{"t1"})
	if got["t1"] != catalog.DefaultPriceProfile {
		t.Fatal("stale продлён отказом сервера", got)
	}
	wait(t, f, "error")
}

func TestCancelledPageDoesNotCancelBackgroundLoad(t *testing.T) {
	f := setup(t, config())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.service.PriceContexts(ctx, buyerID, []ids.TenantID{"t1"})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, f, "ok")
	got, _ := f.service.PriceContexts(context.Background(), buyerID, []ids.TenantID{"t1"})
	if got["t1"] != "marketplace:buyer" {
		t.Fatal(got)
	}
}
