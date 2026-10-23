//go:build integration

package redis_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

func TestOperatorSessionsIsolatedHashedAndExpiring(t *testing.T) {
	ctx := context.Background()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		url = "redis://localhost:26379/0"
	}
	client, err := redis.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	p := prefix()
	sessions := redis.NewOperatorSessions(client, p)
	hash := operator.Hash("opaque-token")
	value := operator.Session{
		TokenHash: hash,
		Principal: operator.Principal{ID: "op1", Role: operator.Role},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := sessions.Save(ctx, value); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Del(context.Background(), p+"operator:session:"+hash).Err() })
	got, err := sessions.Get(ctx, hash)
	if err != nil || got.Principal != value.Principal {
		t.Fatal(got, err)
	}
	if client.Exists(ctx, p+"operator:session:opaque-token").Val() != 0 {
		t.Fatal("сырой токен в ключах")
	}
	ttl := client.PTTL(ctx, p+"operator:session:"+hash).Val()
	if ttl <= 59*time.Minute || ttl > time.Hour {
		t.Fatal(ttl)
	}
	if _, err := redis.NewSessions(client, p).Get(ctx, hash); err == nil {
		t.Fatal("покупательская сессия")
	}
	if err := sessions.Delete(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Get(ctx, hash); !errors.Is(err, operator.ErrUnauthorized) {
		t.Fatal(err)
	}
	value.ExpiresAt = time.Now().Add(20 * time.Millisecond)
	if err := sessions.Save(ctx, value); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := sessions.Get(ctx, hash); !errors.Is(err, operator.ErrUnauthorized) {
		t.Fatal(err)
	}
}
