//go:build integration

package redis_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

func prefix() string {
	return fmt.Sprintf("test%d:", time.Now().UnixNano())
}

func connect(t *testing.T) (*redis.Sessions, *redis.Limiter, func(string) time.Duration) {
	t.Helper()

	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		url = "redis://localhost:26379/0"
	}

	client, err := redis.Open(context.Background(), url)
	if err != nil {
		t.Fatalf("Redis для тестов: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	p := prefix()
	ttl := func(key string) time.Duration { return client.PTTL(context.Background(), p+key).Val() }

	return redis.NewSessions(client, p), redis.NewLimiter(client, p), ttl
}

func TestSessions(t *testing.T) {
	sessions, _, ttl := connect(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	session := buyer.NewSession("token", "6f1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b", now, time.Hour)
	if err := sessions.Save(ctx, session); err != nil {
		t.Fatal(err)
	}

	got, err := sessions.Get(ctx, session.TokenHash)
	if err != nil || got.BuyerID != session.BuyerID || !got.ExpiresAt.Equal(session.ExpiresAt) {
		t.Fatalf("сессия после хранения: %+v %v", got, err)
	}

	if left := ttl("session:" + session.TokenHash); left <= 59*time.Minute || left > time.Hour {
		t.Fatalf("срок жизни ключа — до конца сессии: %s", left)
	}

	if err := sessions.Delete(ctx, session.TokenHash); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Get(ctx, session.TokenHash); !errors.Is(err, buyer.ErrSessionNotFound) {
		t.Fatalf("после удаления: %v", err)
	}

	short := buyer.NewSession("short", "6f1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b", time.Now(), 50*time.Millisecond)
	_ = sessions.Save(ctx, short)
	time.Sleep(120 * time.Millisecond)
	if _, err := sessions.Get(ctx, short.TokenHash); !errors.Is(err, buyer.ErrSessionNotFound) {
		t.Fatalf("истёкшая сессия исчезает сама: %v", err)
	}
}

func TestLimiter(t *testing.T) {
	_, limiter, ttl := connect(t)
	ctx := context.Background()

	for i := range 3 {
		if ok, _, err := limiter.Allow(ctx, "login:phone:7925", 3, time.Minute); err != nil || !ok {
			t.Fatalf("попытка %d в пределах лимита: %v %v", i, ok, err)
		}
	}

	ok, retry, err := limiter.Allow(ctx, "login:phone:7925", 3, time.Minute)
	if err != nil || ok || retry <= 0 || retry > time.Minute {
		t.Fatalf("лимит исчерпан до конца окна: %v %s %v", ok, retry, err)
	}
	if left := ttl("ratelimit:login:phone:7925"); left <= 0 {
		t.Fatalf("у счётчика есть срок жизни: %s", left)
	}

	if ok, _, _ := limiter.Allow(ctx, "login:phone:other", 3, time.Minute); !ok {
		t.Fatal("другой ключ — свой счётчик")
	}

	if ok, _, _ := limiter.Allow(ctx, "short", 1, 50*time.Millisecond); !ok {
		t.Fatal("первая попытка")
	}
	time.Sleep(120 * time.Millisecond)
	if ok, _, _ := limiter.Allow(ctx, "short", 1, 50*time.Millisecond); !ok {
		t.Fatal("после окна счётчик начинается заново")
	}
}
