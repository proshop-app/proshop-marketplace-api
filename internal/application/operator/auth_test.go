package operator_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appoperator "github.com/proshop-app/proshop-marketplace/internal/application/operator"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
)

func TestSessionRotationExpiryRevocationAndRole(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	clock := fake.NewClock(now)
	sessions := fake.NewOperatorSessions()
	cfg := appoperator.Config{
		Principal:    operator.Principal{ID: "op1", Email: "op@example.test", Name: "Оператор", Role: operator.Role},
		PasswordHash: "fake:secret",
		SessionTTL:   time.Hour,
	}
	auth := appoperator.NewAuth(sessions, fake.NewLimiter(), fake.Passwords{}, &fake.IDs{}, clock, cfg)
	if _, _, err := auth.Login(ctx, cfg.Principal.Email, "bad", "ip", ""); !errors.Is(err, operator.ErrUnauthorized) {
		t.Fatal(err)
	}
	principal, token, err := auth.Login(ctx, cfg.Principal.Email, "secret", "ip", "")
	if err != nil || principal != cfg.Principal || token == "" {
		t.Fatalf("вход: %+v %v", principal, err)
	}
	stored, err := sessions.Get(ctx, operator.Hash(token))
	if err != nil || stored.TokenHash == token {
		t.Fatal("в хранилище нужен отпечаток", err)
	}
	_, rotated, err := auth.Login(ctx, cfg.Principal.Email, "secret", "ip", token)
	if err != nil || rotated == token {
		t.Fatal("ротация", err)
	}
	if _, err := auth.Current(ctx, token); !errors.Is(err, operator.ErrUnauthorized) {
		t.Fatal("старый токен", err)
	}
	if _, err := auth.Current(ctx, rotated); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)
	if _, err := auth.Current(ctx, rotated); !errors.Is(err, operator.ErrUnauthorized) {
		t.Fatal("срок", err)
	}
	_, token, err = auth.Login(ctx, cfg.Principal.Email, "secret", "ip", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg.PasswordHash = "fake:new-secret"
	changed := appoperator.NewAuth(sessions, fake.NewLimiter(), fake.Passwords{}, &fake.IDs{}, clock, cfg)
	if _, err := changed.Current(ctx, token); !errors.Is(err, operator.ErrUnauthorized) {
		t.Fatal("отзыв", err)
	}
	if err := auth.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Current(ctx, token); !errors.Is(err, operator.ErrUnauthorized) {
		t.Fatal("выход", err)
	}
	cfg.Principal.Role = "viewer"
	wrongRole := appoperator.NewAuth(sessions, fake.NewLimiter(), fake.Passwords{}, &fake.IDs{}, clock, cfg)
	_, _, err = wrongRole.Login(ctx, cfg.Principal.Email, "new-secret", "ip", "")
	if !errors.Is(err, operator.ErrForbidden) {
		t.Fatal("роль", err)
	}
	if err := sessions.Save(ctx, operator.Session{
		TokenHash:      operator.Hash("viewer-token"),
		Principal:      cfg.Principal,
		CredentialHash: operator.Hash(cfg.PasswordHash),
		ExpiresAt:      clock.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := wrongRole.Current(ctx, "viewer-token"); !errors.Is(err, operator.ErrForbidden) {
		t.Fatal(err)
	}
}

func TestLoginLimits(t *testing.T) {
	auth := appoperator.NewAuth(
		fake.NewOperatorSessions(),
		fake.NewLimiter(),
		fake.Passwords{},
		&fake.IDs{},
		fake.NewClock(time.Now()),
		appoperator.Config{PasswordHash: "fake:secret"},
	)
	for i := 0; i < 10; i++ {
		_, _, err := auth.Login(context.Background(), "unknown@example.test", "bad", "ip", "")
		if !errors.Is(err, operator.ErrUnauthorized) {
			t.Fatal(err)
		}
	}
	_, _, err := auth.Login(context.Background(), "unknown@example.test", "bad", "ip", "")
	if !errors.Is(err, operator.ErrLimited) {
		t.Fatal(err)
	}
}
