package yandex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

func replay(t *testing.T, file string, status int) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/api/b2b/platform/pickup-points/list" || r.Header.Get("Authorization") != "Bearer token" ||
			r.Header.Get("Accept-Language") != "ru" || string(body) != `{"type":"pickup_point"}` {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		w.WriteHeader(status)
		if file != "" {
			data, err := os.ReadFile(filepath.Join("testdata", file))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(data)
		}
	}))
	t.Cleanup(server.Close)

	return server
}

func TestFetch(t *testing.T) {
	provider := New(Config{BaseURL: replay(t, "pickup_points_list.json", http.StatusOK).URL, Token: "token"})

	var points []delivery.PickupPoint
	err := provider.Fetch(context.Background(), func(batch []delivery.PickupPoint) error {
		points = append(points, batch...)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(points) != 2 {
		t.Fatalf("пункт без кода пропускается: %d", len(points))
	}

	first := points[0]
	if first.Carrier != "yandex" ||
		first.Code != "0a1b2c3d4e5f60718293a4b5c6d7e8f9" ||
		first.City != "Город" ||
		first.Address != "Россия, Город, ул. Примерная, 5" ||
		first.Location.Lon != 37.61 ||
		first.Schedule != "Пн-Пт 10:00-21:00; Сб-Вс 10:30-18:00" {
		t.Fatalf("разбор пункта: %+v", first)
	}
	if points[1].Schedule != "Пн-Вс 00:00-23:59" || len(points[1].Payload) == 0 {
		t.Fatalf("круглосуточный пункт: %+v", points[1])
	}
	if provider.Carrier().Name != "Яндекс Доставка" {
		t.Fatal(provider.Carrier())
	}
}

func TestFetchErrors(t *testing.T) {
	unauthorized := New(Config{BaseURL: replay(t, "", http.StatusUnauthorized).URL, Token: "token"})
	err := unauthorized.Fetch(context.Background(), func([]delivery.PickupPoint) error { return nil })
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("отказ API: %v", err)
	}

	wrongToken := New(Config{BaseURL: replay(t, "pickup_points_list.json", http.StatusOK).URL, Token: "other"})
	err = wrongToken.Fetch(context.Background(), func([]delivery.PickupPoint) error { return nil })
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("чужой токен: %v", err)
	}

	stop := errors.New("стоп")
	provider := New(Config{BaseURL: replay(t, "pickup_points_list.json", http.StatusOK).URL, Token: "token"})
	err = provider.Fetch(context.Background(), func([]delivery.PickupPoint) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("ошибка получателя прерывает выгрузку: %v", err)
	}
}
