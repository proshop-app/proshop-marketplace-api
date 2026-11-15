//go:build integration

package redis_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestPriceContextCacheCapacityTTLAndCompareToken(t *testing.T) {
	ctx := context.Background()
	address := os.Getenv("TEST_REDIS_URL")
	if address == "" {
		address = "redis://localhost:26379/0"
	}
	client, err := redis.Open(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	namespace := prefix()
	cache := redis.NewPriceContexts(client, namespace, 2, nil)
	t.Cleanup(func() { _ = cache.Close() })
	t.Cleanup(func() {
		_ = client.Del(
			ctx,
			namespace+"{price-contexts}:b1",
			namespace+"{price-contexts}:b2",
			namespace+"{price-contexts}:b3",
			namespace+"{price-contexts}:active",
		).Err()
	})
	entry := ports.PriceContextEntry{
		LoadedAt: time.Now().UTC(),
		Profiles: map[ids.TenantID]string{"t1": "marketplace:default"},
	}
	for _, buyer := range []ids.UUID{"b1", "b2", "b3"} {
		if err := cache.Begin(ctx, buyer, "token", time.Minute); err != nil {
			t.Fatal(err)
		}
		if saved, err := cache.Store(ctx, buyer, "token", entry, time.Minute); err != nil || !saved {
			t.Fatal(saved, err)
		}
	}
	if _, found, err := cache.Get(ctx, "b1"); err != nil || found {
		t.Fatal("не ограничен размер Redis", found, err)
	}
	if err := cache.Delete(ctx, "b2"); err != nil {
		t.Fatal(err)
	}
	if saved, err := cache.Store(ctx, "b2", "token", entry, time.Minute); err != nil || saved {
		t.Fatal("удаление не отменило загрузку", saved, err)
	}
	key := namespace + "{price-contexts}:b3"
	if ttl, err := client.PTTL(ctx, key).Result(); err != nil || ttl <= 0 || ttl > time.Minute {
		t.Fatal(ttl, err)
	}
	if err := client.PExpireAt(ctx, key, time.Now().Add(-time.Second)).Err(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := cache.Get(ctx, "b3"); err != nil || found {
		t.Fatal("возвращён истёкший контекст", found, err)
	}
}
