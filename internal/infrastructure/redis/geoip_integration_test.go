//go:build integration

package redis_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

func TestGeoIPCacheAndTTL(t *testing.T) {
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
	source := &fake.GeoIP{Region: &buyer.Region{Code: "RU-AD", Name: "Регион"}}
	cache := redis.NewGeoIP(client, source, p, "test-secret")
	ip := "192.0.2.1"
	hash := hmac.New(sha256.New, []byte("test-secret"))
	_, _ = hash.Write([]byte("192.0.2.0/24"))
	key := p + "geoip:" + hex.EncodeToString(hash.Sum(nil))
	defer client.Del(ctx, key)
	for _, address := range []string{ip, "192.0.2.254"} {
		result, err := cache.Locate(ctx, address)
		if err != nil || result == nil || result.Code != "RU-AD" {
			t.Fatal(result, err)
		}
	}
	if source.Calls != 1 {
		t.Fatal("кэш не использован")
	}
	if err := client.Set(ctx, p+"geoip:down", "1", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	region, err := cache.Locate(ctx, ip)
	if err != nil || region == nil || region.Code != "RU-AD" || source.Calls != 1 {
		t.Fatal("кэш должен возвращаться и при down", region, err, source.Calls)
	}
	if err := client.Del(ctx, p+"geoip:down").Err(); err != nil {
		t.Fatal(err)
	}
	ttl, err := client.TTL(ctx, key).Result()
	if err != nil || ttl < 23*time.Hour || ttl > 24*time.Hour {
		t.Fatal(ttl, err)
	}
	if err := client.Del(ctx, key).Err(); err != nil {
		t.Fatal(err)
	}
	source.Region = nil
	for i := 0; i < 2; i++ {
		result, err := cache.Locate(ctx, ip)
		if err != nil || result != nil {
			t.Fatal(result, err)
		}
	}
	if source.Calls != 2 {
		t.Fatal("пустой результат не кэшируется")
	}
	if err := client.Del(ctx, key).Err(); err != nil {
		t.Fatal(err)
	}
	source.Err = errors.New("provider unavailable")
	if _, err := cache.Locate(ctx, ip); err == nil {
		t.Fatal("нет ошибки провайдера")
	}
	if region, err := cache.Locate(ctx, "198.51.100.1"); err != nil || region != nil {
		t.Fatal(region, err)
	}
	if source.Calls != 3 {
		t.Fatal("сбой не кэширован", source.Calls)
	}
	ttl, err = client.TTL(ctx, p+"geoip:down").Result()
	if err != nil || ttl <= 0 || ttl > time.Minute {
		t.Fatal(ttl, err)
	}
	otherSecret := redis.NewGeoIP(client, source, p, "another-secret")
	if _, err := otherSecret.Locate(ctx, ip); err != nil || source.Calls != 3 {
		t.Fatal("пауза провайдера должна действовать независимо от секрета", err)
	}
	noSecret := redis.NewGeoIP(client, source, p, "")
	if _, err := noSecret.Locate(ctx, ip); err != nil || source.Calls != 3 {
		t.Fatal(err, source.Calls)
	}
	if err := client.Del(ctx, p+"geoip:down").Err(); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, _ = cache.Locate(canceled, ip)
	if exists, err := client.Exists(ctx, p+"geoip:down").Result(); err != nil || exists != 0 {
		t.Fatal(exists, err)
	}
}
