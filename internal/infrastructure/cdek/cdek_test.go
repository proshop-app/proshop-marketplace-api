package cdek

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

func replay(t *testing.T, pages map[string]string, tokens *int) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := func(file string) {
			body, err := os.ReadFile(filepath.Join("testdata", file))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(body)
		}

		switch r.URL.Path {
		case "/v2/oauth/token":
			_ = r.ParseForm()
			if r.PostForm.Get("grant_type") != "client_credentials" || r.PostForm.Get("client_id") != "id" || r.PostForm.Get("client_secret") != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_client"}`))

				return
			}
			*tokens++
			send("oauth_token.json")
		case "/v2/deliverypoints":
			if r.Header.Get("Authorization") != "Bearer eyJhbGciOiJSUzI1NiJ9.test" || r.URL.Query().Get("type") != "ALL" {
				w.WriteHeader(http.StatusUnauthorized)

				return
			}

			file, ok := pages[r.URL.Query().Get("page")]
			if !ok {
				file = "deliverypoints_empty.json"
			}
			send(file)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	return server
}

func TestFetchPages(t *testing.T) {
	tokens := 0
	server := replay(t, map[string]string{"0": "deliverypoints_page0.json", "1": "deliverypoints_page1.json"}, &tokens)
	provider := New(Config{BaseURL: server.URL, ClientID: "id", ClientSecret: "secret", PageSize: 2})

	var points []delivery.PickupPoint
	err := provider.Fetch(context.Background(), func(batch []delivery.PickupPoint) error {
		points = append(points, batch...)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(points) != 3 {
		t.Fatalf("пункт без координат пропускается: %d", len(points))
	}
	first := points[0]
	if first.Carrier != "cdek" || first.Code != "MSK123" || first.Name != "На Примерной" || first.City != "Город" ||
		first.Address != "101000, Россия, Город, ул. Примерная, 1" || first.Location.Lat != 55.755814 || first.Schedule == "" || len(first.Payload) == 0 {
		t.Fatalf("разбор пункта: %+v", first)
	}
	if points[1].Name != "пр. Образцовый, 7" || points[1].Address != "пр. Образцовый, 7" {
		t.Fatalf("без названия и полного адреса — короткий адрес: %+v", points[1])
	}
	if tokens != 1 {
		t.Fatalf("токен переиспользуется: %d", tokens)
	}
	if provider.Carrier().Name != "СДЭК" {
		t.Fatal(provider.Carrier())
	}
}

func TestTokenRefresh(t *testing.T) {
	tokens := 0
	server := replay(t, map[string]string{}, &tokens)
	provider := New(Config{BaseURL: server.URL, ClientID: "id", ClientSecret: "secret"})
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	provider.now = func() time.Time { return now }

	noop := func([]delivery.PickupPoint) error { return nil }
	_ = provider.Fetch(context.Background(), noop)
	now = now.Add(58 * time.Minute)
	_ = provider.Fetch(context.Background(), noop)
	now = now.Add(time.Minute)
	_ = provider.Fetch(context.Background(), noop)

	if tokens != 2 {
		t.Fatalf("токен обновляется за минуту до конца: %d", tokens)
	}
}

func TestFetchErrors(t *testing.T) {
	tokens := 0
	server := replay(t, map[string]string{"0": "deliverypoints_page0.json"}, &tokens)

	wrong := New(Config{BaseURL: server.URL, ClientID: "id", ClientSecret: "wrong"})
	if err := wrong.Fetch(context.Background(), func([]delivery.PickupPoint) error { return nil }); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("неверные учётные данные: %v", err)
	}

	stop := errors.New("стоп")
	provider := New(Config{BaseURL: server.URL, ClientID: "id", ClientSecret: "secret", PageSize: 2})
	if err := provider.Fetch(context.Background(), func([]delivery.PickupPoint) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("ошибка получателя прерывает выгрузку: %v", err)
	}
}
