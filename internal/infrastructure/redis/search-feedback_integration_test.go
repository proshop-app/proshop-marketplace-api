//go:build integration

package redis_test

import (
	"context"
	"os"
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
	instances := []*searchfeedback.Feedback{
		searchfeedback.NewFeedback(nil, redis.NewLimiter(first, prefix)),
		searchfeedback.NewFeedback(nil, redis.NewLimiter(second, prefix)),
	}
	const ip = "198.51.100.2"
	const anotherIP = "198.51.100.3"
	for index := range 120 {
		if !instances[index%len(instances)].Allow(ctx, ip) {
			t.Fatalf("запрос %d в пределах лимита", index+1)
		}
	}
	for _, instance := range instances {
		if instance.Allow(ctx, ip) || !instance.Allow(ctx, anotherIP) {
			t.Fatal("обе копии должны видеть общий лимит для каждого адреса")
		}
	}
	if ttl := first.PTTL(ctx, prefix+"ratelimit:search:feedback:"+ip); ttl.Err() != nil || ttl.Val() <= 0 {
		t.Fatal("счётчик должен истекать", ttl)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if instances[0].Allow(ctx, anotherIP) || !instances[1].Allow(ctx, anotherIP) {
		t.Fatal("сбой подключения закрывает приём только на затронутой копии")
	}
}
