package buyer_test

import (
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
)

var t0 = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

func TestParsePhone(t *testing.T) {
	cases := map[string]buyer.Phone{
		"+7 (925) 123-45-67": "79251234567",
		"8 925 123 45 67":    "79251234567",
		"79251234567":        "79251234567",
		"+375 29 123 45 67":  "375291234567",
	}

	for raw, want := range cases {
		got, err := buyer.ParsePhone(raw)
		if err != nil || got != want {
			t.Fatalf("%q: %q, %v", raw, got, err)
		}
	}

	for _, raw := range []string{"", "12345", "+7 925 abc", "0925123456789", "1234567890123456"} {
		if _, err := buyer.ParsePhone(raw); !errors.Is(err, buyer.ErrInvalidPhone) {
			t.Fatalf("%q должен быть отвергнут: %v", raw, err)
		}
	}

	if masked := buyer.Phone("79251234567").Masked(); masked != "+7********67" {
		t.Fatalf("маска: %q", masked)
	}
}

func challenge() *buyer.Challenge {
	c := buyer.NewChallenge(
		"6f1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b",
		"79251234567",
		buyer.CallInstruction{
			ProviderRef:     "201737-542",
			CallPhone:       "78005008275",
			CallPhonePretty: "+7 (800) 500-8275",
		},
		t0,
		5*time.Minute,
	)
	c.MarkSaved()

	return buyer.RestoreChallenge(c.State())
}

func TestChallengeConfirmAndUse(t *testing.T) {
	c := challenge()

	if !c.DueForCheck(t0, 2*time.Second) {
		t.Fatal("новую проверку можно спросить у шлюза")
	}
	if !c.Observe(buyer.CallPending, t0) || c.Status() != buyer.ChallengePending || c.State().Checks != 1 {
		t.Fatalf("ожидание: %+v", c.State())
	}
	if c.DueForCheck(t0.Add(time.Second), 2*time.Second) {
		t.Fatal("шлюз не спрашивают чаще интервала")
	}

	if !c.Observe(buyer.CallConfirmed, t0.Add(30*time.Second)) || c.Status() != buyer.ChallengeConfirmed {
		t.Fatalf("подтверждение: %+v", c.State())
	}
	if status, version := c.Origin(); status != buyer.ChallengePending || version != 1 || c.State().Version != 2 {
		t.Fatalf("исходное состояние для условного обновления: %s %d", status, version)
	}
	c.MarkSaved()

	if err := c.Use(t0.Add(time.Minute)); err != nil || c.Status() != buyer.ChallengeUsed {
		t.Fatalf("выдача сессии: %v %+v", err, c.State())
	}
	if err := c.Use(t0.Add(time.Minute)); !errors.Is(err, buyer.ErrChallengeUsed) {
		t.Fatalf("второй раз проверка не срабатывает: %v", err)
	}
}

func TestChallengeExpiry(t *testing.T) {
	c := challenge()

	if err := c.Use(t0); !errors.Is(err, buyer.ErrChallengeNotConfirmed) {
		t.Fatalf("без звонка сессии нет: %v", err)
	}

	late := t0.Add(6 * time.Minute)
	if !c.Observe(buyer.CallConfirmed, late) || c.Status() != buyer.ChallengeExpired {
		t.Fatalf("звонок после срока не принимается: %+v", c.State())
	}
	if err := c.Use(late); !errors.Is(err, buyer.ErrChallengeExpired) {
		t.Fatalf("истёкшая проверка: %v", err)
	}
	if c.Observe(buyer.CallConfirmed, late) {
		t.Fatal("закрытая проверка не меняется")
	}

	byProvider := challenge()
	if !byProvider.Observe(buyer.CallExpired, t0) || byProvider.Status() != buyer.ChallengeExpired {
		t.Fatalf("шлюз закрыл по сроку: %+v", byProvider.State())
	}

	idle := challenge()
	if idle.Expire(t0) || !idle.Expire(t0.Add(5*time.Minute)) {
		t.Fatal("ждущая проверка закрывается ровно по сроку")
	}
}

func TestSession(t *testing.T) {
	session := buyer.NewSession("secret-token", "6f1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b", t0, time.Hour)

	if session.TokenHash == "secret-token" || session.TokenHash != buyer.HashToken("secret-token") ||
		len(session.TokenHash) != 64 {
		t.Fatalf("хранится отпечаток, а не токен: %q", session.TokenHash)
	}
	if session.Expired(t0.Add(59*time.Minute)) || !session.Expired(t0.Add(time.Hour)) {
		t.Fatal("срок сессии")
	}

	b := buyer.New("6f1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b", "79251234567", t0)
	if b.Phone != "79251234567" || !b.CreatedAt.Equal(t0) {
		t.Fatalf("покупатель: %+v", b)
	}
}
