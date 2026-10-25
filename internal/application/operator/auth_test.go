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
	clock := fake.NewClock(time.Now())
	tokens := fake.NewIdentityTokens(clock)
	cfg := appoperator.Config{
		Principal: operator.Principal{
			ID:    "op1",
			Email: "op@example.test",
			Name:  "Оператор",
			Role:  operator.Role,
		},
		PasswordHash: "fake:secret",
		SessionTTL:   time.Hour,
	}
	auth := appoperator.NewAuth(tokens, fake.NewLimiter(), fake.Passwords{}, &fake.IDs{}, clock, cfg)
	if _, _, err := auth.Login(ctx, cfg.Principal.Email, "bad", "ip", ""); !errors.Is(err, operator.ErrUnauthorized) {
		t.Fatal(err)
	}
	principal, access, err := auth.Login(ctx, cfg.Principal.Email, "secret", "ip", "")
	if err != nil || principal != cfg.Principal || access.Token == "" || access.Key == "" {
		t.Fatalf("вход: %+v %v", principal, err)
	}
	_, rotated, err := auth.Login(ctx, cfg.Principal.Email, "secret", "ip", access.Key)
	if err != nil || rotated.Token == access.Token {
		t.Fatal("повторный вход", err)
	}
	if _, _, err := auth.Refresh(ctx, access.Key); err == nil {
		t.Fatal("предыдущий refresh не отозван")
	}
	if _, err := auth.Current(ctx, access.Token); err != nil {
		t.Fatal("старый JWT действует до exp", err)
	}
	clock.Advance(6 * time.Minute)
	if _, err := auth.Current(ctx, rotated.Token); !errors.Is(err, operator.ErrUnauthorized) {
		t.Fatal("срок JWT", err)
	}
	_, renewed, err := auth.Profile(ctx, rotated.Token, rotated.Key)
	if err != nil {
		t.Fatal("silent profile", err)
	}
	cfg.PasswordHash = "fake:new-secret"
	changed := appoperator.NewAuth(tokens, fake.NewLimiter(), fake.Passwords{}, &fake.IDs{}, clock, cfg)
	if _, err := changed.Current(ctx, renewed.Token); !errors.Is(err, operator.ErrUnauthorized) {
		t.Fatal("смена пароля отзывает старый local subject", err)
	}
	if err := auth.Logout(ctx, renewed.Key); err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.Refresh(ctx, renewed.Key); err == nil {
		t.Fatal("refresh после выхода")
	}
	if _, err := auth.Current(ctx, renewed.Token); err != nil {
		t.Fatal("JWT после выхода действует до exp", err)
	}
	cfg.Principal.Role = "viewer"
	wrongRole := appoperator.NewAuth(tokens, fake.NewLimiter(), fake.Passwords{}, &fake.IDs{}, clock, cfg)
	_, _, err = wrongRole.Login(ctx, cfg.Principal.Email, "new-secret", "ip", "")
	if !errors.Is(err, operator.ErrForbidden) {
		t.Fatal("роль", err)
	}
}

func TestLoginLimits(t *testing.T) {
	auth := appoperator.NewAuth(
		fake.NewIdentityTokens(fake.NewClock(time.Now())),
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
