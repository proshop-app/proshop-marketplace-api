//go:build integration

package redis_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/searchfeedback"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

func TestSearchFeedbackLimiterSharedAcrossReplicas(t *testing.T) {
	address := os.Getenv("TEST_REDIS_URL")
	if address == "" {
		t.Skip("нужен адрес изолированного Redis в TEST_REDIS_URL")
	}
	ctx := context.Background()
	prefix := prefix()
	first, err := redis.Open(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := redis.Open(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	keysForFeedback := searchfeedback.NewBuyerKeys(strings.Repeat("k", 32))
	instances := []*searchfeedback.Feedback{
		searchfeedback.NewFeedback(nil, redis.NewLimiter(first, prefix), keysForFeedback),
		searchfeedback.NewFeedback(nil, redis.NewLimiter(second, prefix), keysForFeedback),
	}
	const ip = "2001:db8:abcd:1::2"
	const sameNetwork = "2001:db8:abcd:1::ffff"
	const anotherIP = "2001:db8:abcd:2::3"
	for index := range 120 {
		if !instances[index%len(instances)].Allow(ctx, ip) {
			t.Fatalf("запрос %d в пределах лимита", index+1)
		}
	}
	for _, instance := range instances {
		if instance.Allow(ctx, sameNetwork) || !instance.Allow(ctx, anotherIP) {
			t.Fatal("обе копии должны видеть общий лимит для каждого адреса")
		}
	}
	keys, err := first.Keys(ctx, prefix+"ratelimit:search:feedback:*").Result()
	if err != nil || len(keys) != 2 {
		t.Fatal("должны быть два счётчика по сети", keys, err)
	}
	for _, key := range keys {
		hash := strings.TrimPrefix(key, prefix+"ratelimit:search:feedback:")
		if _, err := hex.DecodeString(hash); err != nil || len(hash) != sha256.Size*2 {
			t.Fatal("ключ лимитера должен содержать только HMAC сети")
		}
		if ttl := first.PTTL(ctx, key); ttl.Err() != nil || ttl.Val() <= 0 {
			t.Fatal("счётчик должен истекать", ttl)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if instances[0].Allow(ctx, anotherIP) || !instances[1].Allow(ctx, anotherIP) {
		t.Fatal("сбой подключения закрывает приём только на затронутой копии")
	}
}
