package smsru

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// gateway — шлюз на записанных ответах: путь → файл testdata.
func gateway(t *testing.T, replies map[string]string) (*Client, *[]url.Values) {
	t.Helper()

	var forms []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("запрос формой POST: %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		_ = r.ParseForm()
		forms = append(forms, r.PostForm)

		file, ok := replies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)

			return
		}

		body, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	return New(Config{BaseURL: server.URL, APIID: "test-key"}), &forms
}

func TestStart(t *testing.T) {
	client, forms := gateway(t, map[string]string{"/callcheck/add": "callcheck_add_ok.json"})

	call, err := client.Start(context.Background(), "79251234567", "203.0.113.5")
	if err != nil || call.ProviderRef != "201737-542" || call.CallPhone != "78005008275" || call.CallPhonePretty != "+7 (800) 500-8275" {
		t.Fatalf("проверка: %+v %v", call, err)
	}

	form := (*forms)[0]
	if form.Get("api_id") != "test-key" || form.Get("phone") != "79251234567" || form.Get("json") != "1" || form.Get("ip") != "203.0.113.5" {
		t.Fatalf("форма запроса: %v", form)
	}

	if _, err := client.Start(context.Background(), "79251234567", "192.168.1.10"); err != nil {
		t.Fatal(err)
	}
	if (*forms)[1].Has("ip") {
		t.Fatal("частный адрес шлюзу не передаётся")
	}
}

func TestStartRejections(t *testing.T) {
	invalid, _ := gateway(t, map[string]string{"/callcheck/add": "callcheck_add_invalid_phone.json"})
	if _, err := invalid.Start(context.Background(), "79251234567", ""); !errors.Is(err, buyer.ErrInvalidPhone) {
		t.Fatalf("номер без маршрута: %v", err)
	}

	broke, _ := gateway(t, map[string]string{"/callcheck/add": "callcheck_add_no_money.json"})
	if _, err := broke.Start(context.Background(), "79251234567", ""); !errors.Is(err, ErrRejected) || errs.CodeOf(err) != errs.Unavailable {
		t.Fatalf("отказ кодом в теле при HTTP 200: %v", err)
	}

	down, _ := gateway(t, map[string]string{})
	if _, err := down.Start(context.Background(), "79251234567", ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("HTTP-ошибка: %v", err)
	}
}

func TestStatus(t *testing.T) {
	cases := map[string]buyer.CallStatus{
		"callcheck_status_pending.json":   buyer.CallPending,
		"callcheck_status_confirmed.json": buyer.CallConfirmed,
		"callcheck_status_expired.json":   buyer.CallExpired,
	}

	for file, want := range cases {
		client, forms := gateway(t, map[string]string{"/callcheck/status": file})

		got, err := client.Status(context.Background(), "201737-542")
		if err != nil || got != want {
			t.Fatalf("%s: %s %v", file, got, err)
		}
		if (*forms)[0].Get("check_id") != "201737-542" {
			t.Fatalf("идентификатор проверки в запросе: %v", (*forms)[0])
		}
	}

	failing, _ := gateway(t, map[string]string{"/callcheck/status": "callcheck_add_no_money.json"})
	if _, err := failing.Status(context.Background(), "x"); !errors.Is(err, ErrRejected) {
		t.Fatalf("отказ шлюза: %v", err)
	}
}

func TestRegisterCallback(t *testing.T) {
	client, forms := gateway(t, map[string]string{"/callback/add": "callback_add_ok.json"})

	if err := client.RegisterCallback(context.Background(), "https://marketplace.example/api/v1/auth/webhooks/smsru"); err != nil {
		t.Fatal(err)
	}
	if (*forms)[0].Get("url") != "https://marketplace.example/api/v1/auth/webhooks/smsru" {
		t.Fatalf("адрес оповещений: %v", (*forms)[0])
	}
}

func TestSandbox(t *testing.T) {
	sandbox := NewSandbox(10 * time.Second)
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	sandbox.now = func() time.Time { return now }

	call, _ := sandbox.Start(context.Background(), "79251234567", "")
	if status, _ := sandbox.Status(context.Background(), call.ProviderRef); status != buyer.CallPending {
		t.Fatalf("сразу после начала: %s", status)
	}

	now = now.Add(10 * time.Second)
	if status, _ := sandbox.Status(context.Background(), call.ProviderRef); status != buyer.CallConfirmed {
		t.Fatalf("после задержки: %s", status)
	}
	if status, _ := sandbox.Status(context.Background(), "unknown"); status != buyer.CallExpired {
		t.Fatalf("неизвестная проверка: %s", status)
	}
}
