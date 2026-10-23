//go:build integration

package yandex

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

var errEnough = errors.New("первой пачки достаточно")

// TestLiveFetch — живая выгрузка: только с YANDEX_LIVE_TEST=1 и токеном
// YANDEX_DELIVERY_TOKEN (YANDEX_DELIVERY_API_URL — тестовый контур).
func TestLiveFetch(t *testing.T) {
	if os.Getenv("YANDEX_LIVE_TEST") != "1" {
		t.Skip("живой вызов Яндекс Доставки выключен: YANDEX_LIVE_TEST=1 включает")
	}

	provider := New(Config{BaseURL: os.Getenv("YANDEX_DELIVERY_API_URL"), Token: os.Getenv("YANDEX_DELIVERY_TOKEN")})

	var got int
	err := provider.Fetch(context.Background(), func(points []delivery.PickupPoint) error {
		got = len(points)

		return errEnough
	})
	if err != nil && !errors.Is(err, errEnough) || got == 0 {
		t.Fatalf("первая пачка: %d %v", got, err)
	}
}
