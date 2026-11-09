//go:build integration

package redis_test

import (
	"context"
	"io"
	"net"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

func TestStoreCardCacheTTLAndNamespace(t *testing.T) {
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
	p := prefix()
	cache := redis.NewStoreCardCache(client, p)
	if _, hit, err := cache.Get(ctx, "missing"); hit || err != nil {
		t.Fatal(hit, err)
	}
	raw := []byte(`{"items":[{"fromPrice":12345}],"total":1,"limit":20,"offset":0}`)
	if err := cache.Set(ctx, "v1:7:list", raw, 20*time.Minute); err != nil {
		t.Fatal(err)
	}
	key := p + "store-cards:v1:7:list"
	t.Cleanup(func() { _ = client.Del(ctx, key).Err() })
	actual, hit, err := cache.Get(ctx, "v1:7:list")
	if err != nil || !hit || string(actual) != string(raw) {
		t.Fatal(string(actual), hit, err)
	}
	ttl, err := client.PTTL(ctx, key).Result()
	if err != nil || ttl <= 19*time.Minute || ttl > 20*time.Minute {
		t.Fatal(ttl, err)
	}
	if _, hit, err := cache.Get(ctx, "v1:8:list"); hit || err != nil {
		t.Fatal("следующее поколение прочитало старый ответ", hit, err)
	}
	if err := client.PExpire(ctx, key, time.Millisecond).Err(); err != nil {
		t.Fatal(err)
	}
	// Redis удаляет истёкшее значение при чтении, без таймера внутри адаптера.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, hit, err := cache.Get(ctx, "v1:7:list"); err != nil {
			t.Fatal(err)
		} else if !hit {
			return
		}
	}
	t.Fatal("Redis вернул истёкший ответ")
}

func TestStoreCardCacheSlowRedisHasBoundedBudget(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = connection.Close() }()
				_, _ = io.Copy(io.Discard, connection)
			}()
		}
	}()
	client := goredis.NewClient(&goredis.Options{Addr: listener.Addr().String()})
	t.Cleanup(func() { _ = client.Close() })
	cache := redis.NewStoreCardCache(client, "test:")
	started := time.Now()
	_, hit, err := cache.Get(context.Background(), "slow")
	if err == nil || hit || time.Since(started) > time.Second {
		t.Fatal("медленный Redis задержал резервное чтение базы", hit, err, time.Since(started))
	}
}
