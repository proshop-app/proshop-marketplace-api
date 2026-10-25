//go:build integration

package identityhub_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appidentity "github.com/proshop-app/proshop-marketplace/internal/application/identity"
	appoperator "github.com/proshop-app/proshop-marketplace/internal/application/operator"
	"github.com/proshop-app/proshop-marketplace/internal/domain/identity"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/identityhub"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	cache "github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

func TestRealOperatorHubIsolation(t *testing.T) {
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
	url := os.Getenv("TEST_IDENTITY_HUB_URL")
	clientID := "marketplace-operator-test"
	operatorConfig := identityhub.Config{
		URL:      url,
		ClientID: clientID,
		Realm:    "platform",
		Secret:   strings.Repeat("test-operator-secret-", 3),
	}
	gateway := identityhub.New(operatorConfig, &http.Client{Timeout: 5 * time.Second})
	cipher, err := identityhub.NewCipher(
		base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))),
		url+":"+clientID,
	)
	if err != nil {
		t.Fatal(err)
	}
	tokens := appidentity.NewTokens(
		gateway,
		postgres.NewIdentityMappings(pool, url+"/realms/platform", clientID),
		cache.NewIdentityRefreshes(redis, "idh05-operator:"+string(random.IDs{}.NewUUID())+":"),
		cipher,
		random.IDs{},
		clock.System{},
		time.Hour,
	)
	cfg := appoperator.Config{
		Principal: operator.Principal{
			ID:    "operator-local-id",
			Name:  "Оператор",
			Email: "op@example.test",
			Role:  operator.Role,
		},
		PasswordHash: "fake:password",
		SessionTTL:   time.Hour,
	}
	auth := appoperator.NewAuth(tokens, fake.NewLimiter(), fake.Passwords{}, random.IDs{}, clock.System{}, cfg)
	principal, access, err := auth.Login(ctx, cfg.Principal.Email, "password", "", "")
	if err != nil || principal != cfg.Principal {
		t.Fatalf("вход оператора через hub: %v", err)
	}
	t.Cleanup(func() { _ = auth.Logout(context.Background(), access.Key) })
	if _, err := auth.Current(ctx, access.Token); err != nil {
		t.Fatal(err)
	}
	buyerConfig := identityhub.Config{
		URL:      url,
		ClientID: "marketplace-test",
		Realm:    "marketplace",
		Secret:   strings.Repeat("test-marketplace-secret-", 3),
	}
	buyerGateway := identityhub.New(buyerConfig, &http.Client{Timeout: 5 * time.Second})
	if _, err := buyerGateway.Claims(ctx, access.Token, false); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("operator JWT принят в buyer realm: %v", err)
	}
	buyerBundle, err := buyerGateway.Open(ctx, "operator-local-id")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = buyerGateway.Revoke(context.Background(), buyerBundle.Refresh) })
	if _, err := gateway.Claims(ctx, buyerBundle.Access, false); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("buyer JWT принят в operator realm: %v", err)
	}
	time.Sleep(time.Until(access.AccessExpiresAt) + 30*time.Millisecond)
	_, renewed, err := auth.Profile(ctx, access.Token, access.Key)
	if err != nil || renewed.Token == access.Token {
		t.Fatalf("silent profile оператора: %v", err)
	}
	cfg.PasswordHash = "fake:changed"
	changed := appoperator.NewAuth(tokens, fake.NewLimiter(), fake.Passwords{}, random.IDs{}, clock.System{}, cfg)
	if _, err := changed.Current(ctx, renewed.Token); !errors.Is(err, operator.ErrUnauthorized) {
		t.Fatal("старый operator subject принят после смены пароля", err)
	}
	if err := auth.Logout(ctx, access.Key); err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.Refresh(ctx, access.Key); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal("refresh после logout", err)
	}
}
