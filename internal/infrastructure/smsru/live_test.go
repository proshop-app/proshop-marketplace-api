//go:build integration

package smsru

import (
	"context"
	"os"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
)

// TestLiveStart — живой вызов шлюза: только с SMSRU_LIVE_TEST=1, ключом
// SMSRU_API_ID и номером SMSRU_TEST_PHONE. Проверка звонком бесплатна для
// покупателя, но заводит у шлюза настоящую проверку.
func TestLiveStart(t *testing.T) {
	if os.Getenv("SMSRU_LIVE_TEST") != "1" {
		t.Skip("живой вызов sms.ru выключен: SMSRU_LIVE_TEST=1 включает")
	}

	phone, err := buyer.ParsePhone(os.Getenv("SMSRU_TEST_PHONE"))
	if err != nil {
		t.Fatalf("SMSRU_TEST_PHONE: %v", err)
	}

	client := New(Config{APIID: os.Getenv("SMSRU_API_ID")})

	call, err := client.Start(context.Background(), phone, "")
	if err != nil {
		t.Fatal(err)
	}

	if status, err := client.Status(context.Background(), call.ProviderRef); err != nil || status != buyer.CallPending {
		t.Fatalf("новая проверка ждёт звонка: %s %v", status, err)
	}
}
