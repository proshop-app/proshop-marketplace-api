//go:build integration

package identityhub_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appidentity "github.com/proshop-app/proshop-marketplace/internal/application/identity"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/identityhub"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	cache "github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

func TestRealHubBuyerFlow(t *testing.T) {
	if os.Getenv("IDENTITY_HUB_INTEGRATION") != "1" {
		t.Skip("нужен отдельный локальный hub и IDENTITY_HUB_INTEGRATION=1")
	}

	ctx := context.Background()
	pool := pgtest.Pool(t)
	redis, err := cache.Open(ctx, os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = redis.Close() })
	clientID := "marketplace-test"
	hubURL := os.Getenv("TEST_IDENTITY_HUB_URL")
	config := identityhub.Config{
		Realm:    "marketplace",
		URL:      hubURL,
		ClientID: clientID,
		Secret:   strings.Repeat("test-marketplace-secret-", 3),
	}
	gateway := identityhub.New(config, &http.Client{Timeout: 5 * time.Second})
	cipher, err := identityhub.NewCipher(
		base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))),
		hubURL+":"+clientID,
	)
	if err != nil {
		t.Fatal(err)
	}
	mapping := postgres.NewIdentityMappings(pool, hubURL+"/realms/marketplace", clientID)
	prefix := "idh05:" + string(random.IDs{}.NewUUID()) + ":"
	refreshes := cache.NewIdentityRefreshes(redis, prefix)
	tokens := appidentity.NewTokens(gateway, mapping, refreshes, cipher, random.IDs{}, clock.System{}, time.Hour)
	calls := fake.NewCalls()
	auth := appbuyer.NewAuth(
		postgres.NewUnitOfWork(pool),
		calls,
		tokens,
		fake.NewLimiter(),
		random.IDs{},
		clock.System{},
		appbuyer.AuthConfig{
			ChallengeTTL:  time.Minute,
			CheckInterval: time.Nanosecond,
			PhoneLimit:    appbuyer.Limit{Count: 10, Window: time.Minute},
			IPLimit:       appbuyer.Limit{Count: 10, Window: time.Minute},
		},
	)
	challenge, err := auth.StartLogin(ctx, "+7 925 123-45-67", "")
	if err != nil {
		t.Fatal(err)
	}
	calls.Set("ref-1", buyer.CallConfirmed)
	result, err := auth.CheckLogin(ctx, challenge.ID)
	if err != nil || result.Session == nil {
		t.Fatalf("локальная проверка -> hub: %v", err)
	}
	issued := result.Session
	t.Cleanup(func() { _ = tokens.Logout(context.Background(), issued.Key) })
	if len(strings.Split(issued.Token, ".")) != 3 || len(issued.Key) != 43 {
		t.Fatal("нужны настоящий JWT и отдельный случайный браузерный ключ")
	}
	access, err := auth.Authenticate(ctx, issued.Token)
	if err != nil || access.BuyerID != issued.Buyer.ID {
		t.Fatalf("mapping JWT -> локальный buyer: %v", err)
	}
	if _, err := auth.Authenticate(ctx, issued.Key); !errors.Is(err, buyer.ErrSessionNotFound) {
		t.Fatal("ключ refresh принят вместо access")
	}
	keys, err := redis.Keys(ctx, prefix+"*").Result()
	if err != nil || len(keys) != 1 {
		t.Fatalf("ожидалась одна refresh-запись: %v", err)
	}
	raw, _ := redis.Get(ctx, keys[0]).Result()
	if strings.Contains(raw, issued.Token) || strings.Contains(raw, issued.Key) ||
		strings.Contains(raw, "refresh_token") {
		t.Fatal("секрет/Access JWT попал в Redis открытым текстом")
	}

	// Реальное криптографическое истечение, без подмены JWT/часов адаптера.
	time.Sleep(time.Until(issued.AccessExpiresAt) + 30*time.Millisecond)
	if _, err := auth.Authenticate(ctx, issued.Token); !errors.Is(err, buyer.ErrAccessExpired) {
		t.Fatalf("истёкший access должен быть отклонён: %v", err)
	}
	renewed, err := auth.Bootstrap(ctx, issued.Token, issued.Key)
	if err != nil || renewed.Token == issued.Token || renewed.Buyer.ID != issued.Buyer.ID {
		t.Fatalf("silent me: %v", err)
	}
	if _, err := auth.Bootstrap(ctx, "forged.jwt.invalid", issued.Key); !errors.Is(err, buyer.ErrSessionNotFound) {
		t.Fatal("поддельный JWT молча восстановлен")
	}

	// Два экземпляра используют атомарный Redis Claim, без повторения старого refresh.
	second := appidentity.NewTokens(gateway, mapping, refreshes, cipher, random.IDs{}, clock.System{}, time.Hour)
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, service := range []*appidentity.Tokens{tokens, second} {
		wg.Add(1)
		go func(service *appidentity.Tokens) {
			defer wg.Done()
			_, err := service.Refresh(ctx, issued.Key)
			failures <- err
		}(service)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatalf("конкурентный refresh: %v", err)
		}
	}
	if err := auth.Logout(ctx, issued.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Refresh(ctx, issued.Key); !errors.Is(err, buyer.ErrSessionNotFound) {
		t.Fatalf("logout должен закрыть refresh: %v", err)
	}
	if _, err := auth.Authenticate(ctx, renewed.Token); err != nil {
		t.Fatalf("access после logout действует до exp: %v", err)
	}
	if err := redis.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Authenticate(ctx, renewed.Token); err != nil {
		t.Fatalf("JWT access зависит от Redis: %v", err)
	}
}
