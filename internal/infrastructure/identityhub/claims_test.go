package identityhub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
)

type jwtFixture struct {
	key     *ecdsa.PrivateKey
	client  *Client
	server  *httptest.Server
	reads   atomic.Int32
	offline atomic.Bool
}

func newJWTFixture(t *testing.T) *jwtFixture {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	fixture := &jwtFixture{key: key}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.reads.Add(1)
		if fixture.offline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		_ = json.NewEncoder(w).Encode(jwksDocument{Keys: []jwk{{
			KeyID:     "key-one",
			Type:      "EC",
			Curve:     "P-256",
			Algorithm: "ES256",
			Use:       "sig",
			X:         base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))),
			Y:         base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32))),
		}}})
	}))
	t.Cleanup(fixture.server.Close)
	config := Config{
		Realm:    "marketplace",
		URL:      fixture.server.URL,
		ClientID: "market-test",
		Secret:   strings.Repeat("s", 32),
	}
	fixture.client = New(config, fixture.server.Client())

	return fixture
}

func (f *jwtFixture) validClaims() map[string]any {
	return map[string]any{
		"iss":       f.client.issuer,
		"sub":       "hub-subject",
		"aud":       []string{"market-test"},
		"client_id": "market-test",
		"realm":     "marketplace",
		"jti":       "access-id",
		"iat":       time.Now().Unix() - 10,
		"exp":       time.Now().Unix() + 300,
	}
}

func (f *jwtFixture) sign(t *testing.T, claims map[string]any, kid string) string {
	t.Helper()

	head, _ := json.Marshal(map[string]string{
		"alg": "ES256",
		"kid": kid,
	})
	body, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(head) + "." + base64.RawURLEncoding.EncodeToString(body)
	sum := sha256.Sum256([]byte(input))
	first, second, err := ecdsa.Sign(rand.Reader, f.key, sum[:])
	if err != nil {
		t.Fatal(err)
	}

	raw := append(first.FillBytes(make([]byte, 32)), second.FillBytes(make([]byte, 32))...)

	return input + "." + base64.RawURLEncoding.EncodeToString(raw)
}

func TestClaimsBoundaries(t *testing.T) {
	f := newJWTFixture(t)
	ctx := context.Background()
	cases := map[string]any{
		"iss":       "https://foreign.example/realms/marketplace",
		"aud":       []string{"market-test", "other"},
		"client_id": "other",
		"realm":     "platform",
		"tid":       "tenant-id",
		"sub":       "",
		"jti":       "",
		"iat":       time.Now().Unix() + 60,
		"nbf":       time.Now().Unix() + 60,
		"exp":       "not-a-number",
	}
	for field, value := range cases {
		t.Run(field, func(t *testing.T) {
			claims := f.validClaims()
			claims[field] = value
			_, err := f.client.Claims(ctx, f.sign(t, claims, "key-one"), true)
			if !errors.Is(err, buyer.ErrSessionNotFound) {
				t.Fatalf("неверный %s принят: %v", field, err)
			}
		})
	}

	claims := f.validClaims()
	claims["exp"] = time.Now().Unix() - 1
	token := f.sign(t, claims, "key-one")
	if _, err := f.client.Claims(ctx, token, false); !errors.Is(err, buyer.ErrAccessExpired) {
		t.Fatalf("подписанный истёкший access: %v", err)
	}
	if _, err := f.client.Claims(ctx, token, true); err != nil {
		t.Fatalf("bootstrap может проверить истёкшую подпись: %v", err)
	}

	parts := strings.Split(token, ".")
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"forged"}`))
	if _, err := f.client.Claims(ctx, strings.Join(parts, "."), true); !errors.Is(err, buyer.ErrSessionNotFound) {
		t.Fatalf("поддельный JWT: %v", err)
	}
}

func TestKeysOutageAndUnknownKidCooldown(t *testing.T) {
	f := newJWTFixture(t)
	ctx := context.Background()
	token := f.sign(t, f.validClaims(), "key-one")
	if _, err := f.client.Claims(ctx, token, false); err != nil {
		t.Fatal(err)
	}

	f.offline.Store(true)
	f.client.keysUntil = time.Time{}
	if _, err := f.client.Claims(ctx, token, false); err != nil {
		t.Fatalf("известный ключ должен работать без hub: %v", err)
	}

	reads := f.reads.Load()
	for range 10 {
		_, err := f.client.Claims(ctx, f.sign(t, f.validClaims(), "unknown"), false)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("неизвестный ключ при недоступном hub: %v", err)
		}
	}
	if f.reads.Load() != reads {
		t.Fatal("backoff не ограничил загрузку JWKS")
	}
}

func TestMalformedJWTDoesNotReadKeys(t *testing.T) {
	f := newJWTFixture(t)
	for _, token := range []string{"", "opaque-old-token", "a.b.c", "e30.e30.c2ln", strings.Repeat("x", 17000)} {
		if _, err := f.client.Claims(context.Background(), token, true); !errors.Is(err, buyer.ErrSessionNotFound) {
			t.Fatalf("malformed: %v", err)
		}
	}
	if f.reads.Load() != 0 {
		t.Fatal("мусор инициировал JWKS запрос")
	}
}

func TestRefreshCipherBinding(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	cipher, err := NewCipher(key, "issuer:client")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cipher.Seal("refresh-secret", "record-one")
	if err != nil || strings.Contains(sealed, "refresh-secret") {
		t.Fatal("шифрование не выполнено")
	}
	if plain, err := cipher.Open(sealed, "record-one"); err != nil || plain != "refresh-secret" {
		t.Fatal("свой ciphertext не прочитан")
	}
	if _, err := cipher.Open(sealed, "record-two"); err == nil {
		t.Fatal("ciphertext принят для чужой записи")
	}
	other, _ := NewCipher(key, "issuer:other-client")
	if _, err := other.Open(sealed, "record-one"); err == nil {
		t.Fatal("ciphertext принят чужим клиентом")
	}
}
