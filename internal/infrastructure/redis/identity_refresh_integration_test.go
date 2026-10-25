//go:build integration

package redis_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

func identityRefreshFixture(t *testing.T) (*redis.IdentityRefreshes, *goredis.Client, string) {
	t.Helper()

	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		url = "redis://localhost:26379/0"
	}

	client, err := redis.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}

	p := prefix()
	t.Cleanup(func() {
		keys, _ := client.Keys(context.Background(), p+"*").Result()
		if len(keys) > 0 {
			_ = client.Del(context.Background(), keys...).Err()
		}
		_ = client.Close()
	})

	return redis.NewIdentityRefreshes(client, p), client, p + "identity:refresh:key"
}

func TestIdentityRefreshActiveExchangeAndCAS(t *testing.T) {
	store, client, key := identityRefreshFixture(t)
	ctx := context.Background()
	record := ports.IdentityRefresh{
		LocalID:   "one",
		Subject:   "subject-one",
		Encrypted: "ciphertext",
		ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := store.Save(ctx, "key", record); err != nil {
		t.Fatal(err)
	}

	before := client.PTTL(ctx, key).Val()
	if _, err := store.Claim(ctx, "key", "owner-one", "one"); err != nil {
		t.Fatal(err)
	}

	waiting, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := store.Claim(waiting, "key", "owner-two", "one"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("живой обмен остаётся занятым, а не становится новым входом", err)
	}

	record.Encrypted = "rotated-ciphertext"
	if err := store.Finish(ctx, "key", "owner-one", record); err != nil {
		t.Fatal(err)
	}
	got, err := store.Claim(ctx, "key", "owner-two", "one")
	if err != nil || got.Encrypted != record.Encrypted {
		t.Fatal("последующий обмен должен использовать новый refresh", err)
	}
	if after := client.PTTL(ctx, key).Val(); after <= 0 || after > before {
		t.Fatal("ротация не продлевает абсолютный TTL", after, before)
	}

	if _, err := store.Delete(ctx, "key"); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, "key", "owner-two", record); !errors.Is(err, buyer.ErrSessionNotFound) {
		t.Fatal("logout не должен воскреснуть после Finish", err)
	}
}

func TestIdentityRefreshAbandonedExchangeRequiresLogin(t *testing.T) {
	for _, claimedAt := range []int64{0, time.Now().Add(-9 * time.Second).UnixMilli()} {
		t.Run(time.UnixMilli(claimedAt).String(), func(t *testing.T) {
			store, client, key := identityRefreshFixture(t)
			ctx := context.Background()
			record := ports.IdentityRefresh{
				LocalID:   "one",
				Subject:   "subject-one",
				Encrypted: "ciphertext",
				ExpiresAt: time.Now().Add(time.Minute),
			}
			doc := map[string]any{
				"record": record,
				"owner":  "abandoned-owner",
			}
			if claimedAt != 0 {
				doc["claimedAt"] = claimedAt
			}

			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Set(ctx, key, raw, time.Minute).Err(); err != nil {
				t.Fatal(err)
			}

			if _, err := store.Claim(ctx, "key", "foreign-owner", "two"); !errors.Is(err, buyer.ErrSessionNotFound) {
				t.Fatal(err)
			}
			if client.Exists(ctx, key).Val() != 1 {
				t.Fatal("чужой аккаунт не должен изменять даже зависшую запись")
			}
			if _, err := store.Claim(ctx, "key", "next-owner", "one"); !errors.Is(err, buyer.ErrSessionNotFound) {
				t.Fatal("зависший обмен требует входа вместо вечного busy", err)
			}
			if client.Exists(ctx, key).Val() != 0 {
				t.Fatal("неопределённый refresh не должен выдаваться новому владельцу")
			}
			if err := store.Finish(ctx, "key", "abandoned-owner", record); !errors.Is(err, buyer.ErrSessionNotFound) {
				t.Fatal("поздний результат не должен восстанавливать закрытый вход", err)
			}
		})
	}
}
