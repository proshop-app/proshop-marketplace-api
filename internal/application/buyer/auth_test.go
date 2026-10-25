package buyer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

var t0 = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

type env struct {
	store    *fake.Store
	calls    *fake.Calls
	sessions *fake.IdentityTokens
	clock    *fake.Clock
	auth     *appbuyer.Auth
}

func setup() *env {
	e := &env{store: fake.NewStore(), calls: fake.NewCalls(), clock: fake.NewClock(t0)}
	e.sessions = fake.NewIdentityTokens(e.clock)
	e.auth = appbuyer.NewAuth(
		e.store,
		e.calls,
		e.sessions,
		fake.NewLimiter(),
		&fake.IDs{},
		e.clock,
		appbuyer.AuthConfig{
			ChallengeTTL:  5 * time.Minute,
			CheckInterval: 2 * time.Second,
			SessionTTL:    30 * 24 * time.Hour,
			PhoneLimit: appbuyer.Limit{
				Count:  3,
				Window: 10 * time.Minute,
			},
			IPLimit: appbuyer.Limit{Count: 5, Window: time.Hour},
		},
	)

	return e
}

func TestLoginByCall(t *testing.T) {
	e := setup()
	ctx := context.Background()

	view, err := e.auth.StartLogin(ctx, "+7 (925) 123-45-67", "203.0.113.5")
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != buyer.ChallengePending || view.CallPhonePretty != "+7 (800) 500-8275" ||
		!view.ExpiresAt.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("проверка: %+v", view)
	}

	result, err := e.auth.CheckLogin(ctx, view.ID)
	if err != nil || result.Session != nil || result.Challenge.Status != buyer.ChallengePending ||
		e.calls.Asked() != 1 {
		t.Fatalf("звонка ещё не было: %+v %v", result, err)
	}

	e.calls.Set("ref-1", buyer.CallConfirmed)
	e.clock.Advance(time.Second)
	if result, _ := e.auth.CheckLogin(ctx, view.ID); result.Session != nil || e.calls.Asked() != 1 {
		t.Fatal("шлюз не спрашивают чаще интервала")
	}

	e.clock.Advance(2 * time.Second)
	result, err = e.auth.CheckLogin(ctx, view.ID)
	if err != nil || result.Session == nil || result.Session.Token == "" ||
		result.Session.Buyer.Phone != "79251234567" {
		t.Fatalf("звонок подтверждён — сессия: %+v %v", result, err)
	}

	session, err := e.auth.Authenticate(ctx, result.Session.Token)
	if err != nil || session.BuyerID != result.Session.Buyer.ID {
		t.Fatalf("сессия по токену: %+v %v", session, err)
	}

	if _, err := e.auth.CheckLogin(ctx, view.ID); !errors.Is(err, buyer.ErrChallengeUsed) {
		t.Fatalf("по одной проверке — одна сессия: %v", err)
	}

	again, _ := e.auth.StartLogin(ctx, "89251234567", "")
	e.calls.Set("ref-2", buyer.CallConfirmed)
	second, err := e.auth.CheckLogin(ctx, again.ID)
	if err != nil || second.Session.Buyer.ID != result.Session.Buyer.ID || len(e.store.Buyers()) != 1 {
		t.Fatalf("тот же телефон — тот же покупатель: %+v %v", second, err)
	}

	if err := e.auth.Logout(ctx, result.Session.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := e.auth.Authenticate(ctx, result.Session.Token); err != nil {
		t.Fatalf("JWT после выхода действует до exp: %v", err)
	}
	if _, err := e.auth.Refresh(ctx, result.Session.Key); !errors.Is(err, buyer.ErrSessionNotFound) {
		t.Fatalf("после выхода refresh закрыт: %v", err)
	}
	if me, err := e.auth.Me(ctx, second.Session.Buyer.ID); err != nil || me.Phone != "79251234567" {
		t.Fatalf("покупатель сессии: %+v %v", me, err)
	}
}

func TestLoginLimitsAndErrors(t *testing.T) {
	e := setup()
	ctx := context.Background()

	if _, err := e.auth.StartLogin(ctx, "abc", ""); !errors.Is(err, buyer.ErrInvalidPhone) {
		t.Fatalf("неверный номер: %v", err)
	}

	for range 3 {
		if _, err := e.auth.StartLogin(ctx, "79251234567", "203.0.113.5"); err != nil {
			t.Fatal(err)
		}
	}

	_, err := e.auth.StartLogin(ctx, "79251234567", "203.0.113.5")
	if !errors.Is(err, buyer.ErrTooManyAttempts) || errs.CodeOf(err) != errs.TooManyRequests {
		t.Fatalf("лимит по телефону: %v", err)
	}

	for i := range 2 {
		if _, err := e.auth.StartLogin(ctx, "7925123456"+string(rune('0'+i)), "203.0.113.5"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.auth.StartLogin(ctx, "79251234599", "203.0.113.5"); !errors.Is(err, buyer.ErrTooManyAttempts) {
		t.Fatalf("лимит по адресу: %v", err)
	}

	e.calls.StartErr = errors.New("связь")
	if _, err := e.auth.StartLogin(ctx, "79990000000", ""); !errors.Is(err, appbuyer.ErrCallCheckFailed) ||
		errs.CodeOf(err) != errs.Unavailable {
		t.Fatalf("шлюз отказал: %v", err)
	}

	if _, err := e.auth.Authenticate(ctx, ""); !errors.Is(err, buyer.ErrSessionNotFound) {
		t.Fatalf("без токена: %v", err)
	}
}

func TestLoginExpiryAndGatewayFailure(t *testing.T) {
	e := setup()
	ctx := context.Background()

	view, _ := e.auth.StartLogin(ctx, "79251234567", "")

	e.calls.StatusErr = errors.New("связь")
	result, err := e.auth.CheckLogin(ctx, view.ID)
	if err != nil || result.Challenge.Status != buyer.ChallengePending {
		t.Fatalf("сбой шлюза — ещё ждём: %+v %v", result, err)
	}

	e.calls.StatusErr = nil
	e.clock.Advance(6 * time.Minute)
	result, err = e.auth.CheckLogin(ctx, view.ID)
	if err != nil || result.Challenge.Status != buyer.ChallengeExpired || result.Session != nil {
		t.Fatalf("срок вышел: %+v %v", result, err)
	}
	if state, _ := e.store.Challenge(view.ID); state.Status != buyer.ChallengeExpired {
		t.Fatalf("истёкшая проверка сохранена: %+v", state)
	}
}

func TestWebhookRefreshesFromGateway(t *testing.T) {
	e := setup()
	ctx := context.Background()

	view, _ := e.auth.StartLogin(ctx, "79251234567", "")
	e.calls.Set("ref-1", buyer.CallConfirmed)

	if err := e.auth.RefreshByProvider(ctx, "ref-1"); err != nil {
		t.Fatal(err)
	}
	if state, _ := e.store.Challenge(view.ID); state.Status != buyer.ChallengeConfirmed {
		t.Fatalf("оповещение перечитывает состояние у шлюза: %+v", state)
	}
	if err := e.auth.RefreshByProvider(ctx, "unknown"); err != nil {
		t.Fatalf("чужая проверка — не ошибка: %v", err)
	}

	result, err := e.auth.CheckLogin(ctx, view.ID)
	if err != nil || result.Session == nil {
		t.Fatalf("после оповещения опрос выдаёт сессию: %+v %v", result, err)
	}
}

func TestSessionStoreFailure(t *testing.T) {
	e := setup()
	ctx := context.Background()

	view, _ := e.auth.StartLogin(ctx, "79251234567", "")
	e.calls.Set("ref-1", buyer.CallConfirmed)
	e.sessions.SaveErr = errors.New("redis недоступен")

	if _, err := e.auth.CheckLogin(ctx, view.ID); err == nil {
		t.Fatal("сбой хранилища сессий возвращается")
	}
	if e.sessions.Count() != 0 {
		t.Fatal("сессии нет")
	}
}
