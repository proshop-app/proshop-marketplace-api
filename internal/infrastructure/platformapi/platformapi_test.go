package platformapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/platformapi"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const prefix = "/api/v1/internal/marketplace/tenants/t1"

func fixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	return data
}

// sameJSON сравнивает тела по смыслу.
func sameJSON(t *testing.T, got []byte, want []byte) {
	t.Helper()

	var a, b any
	if json.Unmarshal(got, &a) != nil || json.Unmarshal(want, &b) != nil {
		t.Fatalf("не JSON: %s / %s", got, want)
	}

	encodedA, _ := json.Marshal(a)
	encodedB, _ := json.Marshal(b)
	if string(encodedA) != string(encodedB) {
		t.Fatalf("тело запроса не по контракту:\n%s\nожидалось:\n%s", encodedA, encodedB)
	}
}

// server — сервер на записанных примерах контракта: путь → запрос и ответ.
func server(t *testing.T, routes map[string][2]string, status int) *platformapi.Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(platformapi.HeaderToken) != "service-token" {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		route, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			http.NotFound(w, r)

			return
		}

		if route[0] != "" {
			body, _ := io.ReadAll(r.Body)
			sameJSON(t, body, fixture(t, route[0]))
		}

		w.WriteHeader(status)
		_, _ = w.Write(fixture(t, route[1]))
	}))
	t.Cleanup(srv.Close)

	return platformapi.New(platformapi.Config{BaseURL: srv.URL, Token: "service-token", Timeout: time.Second})
}

func TestCheckItems(t *testing.T) {
	client := server(t, map[string][2]string{"POST " + prefix + "/items/check": {"items_check_request.json", "items_check_response.json"}}, http.StatusOK)

	checks, err := client.CheckItems(context.Background(), "t1", []ports.ItemRequest{{ProductID: "p1", Quantity: 2}, {ProductID: "p2", Quantity: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 2 || !checks[0].Exists || checks[0].Price.Kopecks() != 199000 || checks[0].Available != 5 || checks[1].Exists {
		t.Fatalf("ответ: %+v", checks)
	}
}

func TestQuoteDelivery(t *testing.T) {
	request := ports.QuoteRequest{Carrier: "cdek", PickupPointCode: "MSK123", City: "Город", Items: []ports.ItemRequest{{ProductID: "p1", Quantity: 2}}}

	available := server(t, map[string][2]string{"POST " + prefix + "/delivery/quote": {"delivery_quote_request.json", "delivery_quote_available.json"}}, http.StatusOK)
	offered, err := available.QuoteDelivery(context.Background(), "t1", request)
	if err != nil || !offered.Available || offered.Cost.Kopecks() != 30000 || offered.MaxDays != 4 ||
		!offered.ExpiresAt.Equal(time.Date(2026, 10, 24, 10, 30, 0, 0, time.UTC)) {
		t.Fatalf("расчёт: %+v %v", offered, err)
	}

	unavailable := server(t, map[string][2]string{"POST " + prefix + "/delivery/quote": {"delivery_quote_request.json", "delivery_quote_unavailable.json"}}, http.StatusOK)
	offered, err = unavailable.QuoteDelivery(context.Background(), "t1", request)
	if err != nil || offered.Available || offered.Reason != "carrier_not_connected" {
		t.Fatalf("не везёт: %+v %v", offered, err)
	}
}

func TestMarketplaceSettings(t *testing.T) {
	client := server(t, map[string][2]string{"GET " + prefix + "/settings/marketplace": {"", "settings_marketplace.json"}}, http.StatusOK)

	settings, err := client.MarketplaceSettings(context.Background(), "t1")
	if err != nil || !settings.Enabled || settings.Subsidy.Type != participant.SubsidyPercent || settings.Subsidy.Value != 50 || settings.Subsidy.MaxAmount.Kopecks() != 30000 {
		t.Fatalf("настройки: %+v %v", settings, err)
	}
}

func TestErrors(t *testing.T) {
	refused := server(t, map[string][2]string{"GET " + prefix + "/settings/marketplace": {"", "error.json"}}, http.StatusForbidden)
	if _, err := refused.MarketplaceSettings(context.Background(), "t1"); !errors.Is(err, platformapi.ErrUnavailable) || errs.CodeOf(err) != errs.Unavailable {
		t.Fatalf("отказ сервера: %v", err)
	}

	broken := server(t, map[string][2]string{"GET " + prefix + "/settings/marketplace": {"", "items_check_request.json"}}, http.StatusOK)
	if settings, err := broken.MarketplaceSettings(context.Background(), "t1"); err != nil || settings.Enabled {
		t.Fatalf("тело без полей — участие выключено: %+v %v", settings, err)
	}

	wrongToken := platformapi.New(platformapi.Config{BaseURL: "http://127.0.0.1:1", Token: "x", Timeout: 100 * time.Millisecond})
	if _, err := wrongToken.CheckItems(context.Background(), "t1", nil); !errors.Is(err, platformapi.ErrUnavailable) {
		t.Fatalf("сервер не отвечает: %v", err)
	}
}
