//go:build integration

package cdek

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

var errEnough = errors.New("первой страницы достаточно")

// TestLiveFetch — живая выгрузка первой страницы: только с CDEK_LIVE_TEST=1
// и учётной записью CDEK_CLIENT_ID, CDEK_CLIENT_SECRET (CDEK_API_URL — песочница).
func TestLiveFetch(t *testing.T) {
	if os.Getenv("CDEK_LIVE_TEST") != "1" {
		t.Skip("живой вызов СДЭК выключен: CDEK_LIVE_TEST=1 включает")
	}

	provider := New(Config{
		BaseURL: os.Getenv("CDEK_API_URL"), ClientID: os.Getenv("CDEK_CLIENT_ID"), ClientSecret: os.Getenv("CDEK_CLIENT_SECRET"), PageSize: 50,
	})

	var got int
	err := provider.Fetch(context.Background(), func(points []delivery.PickupPoint) error {
		got = len(points)

		return errEnough
	})
	if !errors.Is(err, errEnough) || got == 0 {
		t.Fatalf("первая страница: %d %v", got, err)
	}
}
