package contextcache_test

import (
	"context"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/contextcache"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func put(t *testing.T, cache ports.PriceContextCache, buyer ids.UUID, clock *fake.Clock) {
	t.Helper()
	ctx := context.Background()
	if _, err := cache.Begin(ctx, buyer, "token", time.Minute); err != nil {
		t.Fatal(err)
	}
	saved, err := cache.Store(
		ctx,
		buyer,
		"token",
		ports.PriceContextEntry{
			LoadedAt: clock.Now(),
			Profiles: map[ids.TenantID]string{"t1": "marketplace:buyer"},
		},
		time.Minute,
	)
	if err != nil || !saved {
		t.Fatal(saved, err)
	}
}

func TestMemoryCapacityExpiryAndInvalidation(t *testing.T) {
	ctx := context.Background()
	clock := fake.NewClock(time.Now())
	cache := contextcache.NewMemory(2, clock, nil)
	put(t, cache, "b1", clock)
	put(t, cache, "b2", clock)
	entry, found, err := cache.Get(ctx, "b1")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	entry.Profiles["t1"] = "changed"
	put(t, cache, "b3", clock)
	if _, found, _ := cache.Get(ctx, "b2"); found {
		t.Fatal("не вытеснена самая старая запись")
	}
	entry, _, _ = cache.Get(ctx, "b1")
	if entry.Profiles["t1"] != "marketplace:buyer" {
		t.Fatal("кэш допускает изменение возвращённой карты")
	}
	if err := cache.Delete(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if saved, err := cache.Store(ctx, "b1", "token", entry, time.Minute); err != nil || saved {
		t.Fatal("сброс не отменил запись загрузки", saved, err)
	}
	clock.Advance(time.Minute)
	if _, found, _ := cache.Get(ctx, "b3"); found {
		t.Fatal("истёкший контекст прочитан")
	}
}

func TestFallbackOnPrimaryFailureAndNoResurrectionOnMiss(t *testing.T) {
	ctx := context.Background()
	clock := fake.NewClock(time.Now())
	primary := &fake.PriceContextCache{GetError: errs.New(errs.Unavailable, "cache.failed", "отказ")}
	memory := contextcache.NewMemory(2, clock, nil)
	cache := contextcache.NewFallback(primary, memory)
	put(t, cache, "b1", clock)
	entry, found, err := cache.Get(ctx, "b1")
	if err != nil || !found || entry.Profiles["t1"] != "marketplace:buyer" {
		t.Fatal(entry, found, err)
	}
	primary.GetError = nil
	if err := primary.Delete(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := cache.Get(ctx, "b1"); found {
		t.Fatal("общий сброс оживил старую локальную запись")
	}
	cache = contextcache.NewFallback(nil, memory)
	put(t, cache, "b1", clock)
	if _, found, err := cache.Get(ctx, "b1"); err != nil || !found {
		t.Fatal("режим без Redis", found, err)
	}
}

func TestSharedInvalidationRejectsPendingLocalCopy(t *testing.T) {
	ctx := context.Background()
	clock := fake.NewClock(time.Now())
	primary := &fake.PriceContextCache{}
	memory := contextcache.NewMemory(2, clock, nil)
	cache := contextcache.NewFallback(primary, memory)
	if _, err := cache.Begin(ctx, "b1", "token", time.Minute); err != nil {
		t.Fatal(err)
	}
	// Событие обработала другая реплика, локальный токен пока о нём не знает.
	if err := primary.Delete(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	entry := ports.PriceContextEntry{LoadedAt: clock.Now()}
	if saved, err := cache.Store(ctx, "b1", "token", entry, time.Minute); err != nil || saved {
		t.Fatal(saved, err)
	}
	primary.GetError = errs.New(errs.Unavailable, "cache.failed", "отказ")
	if _, found, err := cache.Get(ctx, "b1"); err != nil || found {
		t.Fatal("запись ожила через резерв после общего сброса", found, err)
	}
}

func TestBeginUsesSharedSnapshotAfterInvalidation(t *testing.T) {
	ctx := context.Background()
	clock := fake.NewClock(time.Now())
	primary := &fake.PriceContextCache{}
	memory := contextcache.NewMemory(2, clock, nil)
	cache := contextcache.NewFallback(primary, memory)
	put(t, cache, "b1", clock)
	entry, err := cache.Begin(ctx, "b1", "refresh", time.Minute)
	if err != nil || entry.Profiles["t1"] != "marketplace:buyer" {
		t.Fatal("атомарный снимок потерян", entry, err)
	}
	if err := primary.Delete(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	entry, err = cache.Begin(ctx, "b1", "after-event", time.Minute)
	if err != nil || !entry.LoadedAt.IsZero() || len(entry.Profiles) != 0 {
		t.Fatal("общий сброс оживил локальный профиль при загрузке", entry, err)
	}
	if _, found, _ := memory.Get(ctx, "b1"); found {
		t.Fatal("сброшенный профиль остался в резерве")
	}
}
