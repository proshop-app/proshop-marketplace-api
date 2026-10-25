//go:build integration

package identityhub_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appidentity "github.com/proshop-app/proshop-marketplace/internal/application/identity"
	appoperator "github.com/proshop-app/proshop-marketplace/internal/application/operator"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/identityhub"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	cache "github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/smsru"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
)

func formTokens(
	t *testing.T,
	pool *pgxpool.Pool,
	redis *goredis.Client,
	realm, client, secret string,
) *appidentity.Tokens {
	t.Helper()
	hubURL := os.Getenv("TEST_IDENTITY_HUB_URL")
	cfg := identityhub.Config{
		URL:      hubURL,
		Realm:    realm,
		ClientID: client,
		Secret:   secret,
	}
	gateway := identityhub.New(cfg, &http.Client{Timeout: 5 * time.Second})
	cipher, err := identityhub.NewCipher(
		base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))),
		hubURL+":"+client,
	)
	if err != nil {
		t.Fatal(err)
	}

	return appidentity.NewTokens(
		gateway,
		postgres.NewIdentityMappings(pool, hubURL+"/realms/"+realm, client),
		cache.NewIdentityRefreshes(redis, "idh05-forms:"+client+":"+string(random.IDs{}.NewUUID())+":"),
		cipher,
		random.IDs{},
		clock.System{},
		time.Hour,
	)
}

// TestBrowserForms предоставляет только auth HTTP граф; Kafka/OpenSearch и заказы не нужны.
// Сам браузерный прогон запускается оператором отдельно, пути артефактов — только временные.
func TestBrowserForms(t *testing.T) {
	if os.Getenv("IDENTITY_FORM_TEST") != "1" {
		t.Skip("явно разрешённый прогон локальных форм: IDENTITY_FORM_TEST=1")
	}
	ctx := context.Background()
	pool := pgtest.Pool(t)
	redis, err := cache.Open(ctx, os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = redis.Close() })
	buyerSecret := strings.Repeat("test-marketplace-secret-", 3)
	buyerTokens := formTokens(t, pool, redis, "marketplace", "marketplace-test", buyerSecret)
	operatorSecret := strings.Repeat("test-operator-secret-", 3)
	operatorTokens := formTokens(t, pool, redis, "platform", "marketplace-operator-test", operatorSecret)
	auth := appbuyer.NewAuth(
		postgres.NewUnitOfWork(pool),
		smsru.NewSandbox(time.Nanosecond),
		buyerTokens,
		fake.NewLimiter(),
		random.IDs{},
		clock.System{},
		appbuyer.AuthConfig{
			ChallengeTTL:  time.Minute,
			CheckInterval: time.Nanosecond,
			PhoneLimit:    appbuyer.Limit{Count: 50, Window: time.Minute},
			IPLimit:       appbuyer.Limit{Count: 50, Window: time.Minute},
		},
	)
	operators := appoperator.NewAuth(
		operatorTokens,
		fake.NewLimiter(),
		fake.Passwords{},
		random.IDs{},
		clock.System{},
		appoperator.Config{
			Principal: operator.Principal{
				ID:    "forms-operator",
				Name:  "Тестовый оператор",
				Email: "op@example.test",
				Role:  operator.Role,
			},
			PasswordHash: "fake:password",
			SessionTTL:   time.Hour,
		},
	)
	var handler http.Handler
	operatorServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			handler.ServeHTTP(w, r)

			return
		}
		root := os.Getenv("TEST_OPERATOR_DIST")
		path := filepath.Join(root, filepath.Clean("/"+r.URL.Path))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			http.ServeFile(w, r, path)

			return
		}
		http.ServeFile(w, r, filepath.Join(root, "index.html"))
	}))
	t.Cleanup(operatorServer.Close)
	handler = transport.NewHandler(transport.Deps{
		Auth:           auth,
		OperatorAuth:   operators,
		OperatorOrigin: operatorServer.URL,
		Registerer:     prometheus.NewRegistry(),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)
	ready := os.Getenv("TEST_FORM_READY_FILE")
	payload, _ := json.Marshal(map[string]string{
		"apiURL":      api.URL,
		"operatorURL": operatorServer.URL,
	})
	if err := os.WriteFile(ready, payload, 0600); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(ready) }()
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("локальный browser runner не завершился вовремя")
		case <-ticker.C:
			if _, err := os.Stat(os.Getenv("TEST_FORM_STOP_FILE")); err == nil {
				return
			}
		}
	}
}
