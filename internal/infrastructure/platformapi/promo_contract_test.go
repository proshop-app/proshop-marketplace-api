package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestPricingRefusalLogsTenantOnSharedPath(t *testing.T) {
	raw, err := os.ReadFile("testdata/order_unknown_error.json")
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	client := New(Config{BaseURL: "http://platform.test", Token: "service-token", Timeout: time.Second, Conns: 4})
	client.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/internal/marketplace/tenants/t1/items/check" {
			t.Error(r.URL)
		}
		w := httptest.NewRecorder()
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write(raw)
		return w.Result(), nil
	})
	if _, err := client.PriceGroup(context.Background(), "t1", ports.GroupPricingRequest{}); err == nil {
		t.Fatal("ожидался отказ")
	}
	if !strings.Contains(logs.String(), `"tenantId":"t1"`) {
		t.Fatal(logs.String())
	}
}

func TestOrderBodyAlwaysIncludesDiscountArray(t *testing.T) {
	body, err := json.Marshal(orderBody(ports.CreateOrderRequest{Items: []ports.OrderItem{{ProductID: "p1"}}}))
	if err != nil || !strings.Contains(string(body), `"discounts":[]`) {
		t.Fatal(string(body), err)
	}
}

func TestTrackingBlankNumberFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/order_tracking_empty_number.json")
	if err != nil {
		t.Fatal(err)
	}
	var answer sellerOrderJSON
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatal(err)
	}
	order, err := answer.order(false)
	if err != nil || order.Tracking == nil || order.Tracking.Number != nil {
		t.Fatal(order, err)
	}
}

func TestItemsCheckOmitsBuyerAndEmptyPromo(t *testing.T) {
	raw, err := os.ReadFile("testdata/items_check_no_code.json")
	if err != nil {
		t.Fatal(err)
	}
	client := New(Config{BaseURL: "http://platform.test", Token: "service-token", Timeout: time.Second, Conns: 4})
	client.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/internal/marketplace/tenants/t1/items/check" {
			t.Fatal(r.Method, r.URL)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 1 || body["items"] == nil {
			t.Fatal("buyerId и пустой promoCode должны отсутствовать", body)
		}
		response := httptest.NewRecorder()
		_, _ = response.Write(raw)
		return response.Result(), nil
	})
	result, err := client.PriceGroup(context.Background(), "t1", ports.GroupPricingRequest{
		Items: []ports.ItemRequest{{ProductID: "p1", Quantity: 3}},
	})
	if err != nil || len(result.Items) != 1 || result.Items[0].Price.Kopecks() != 100 ||
		len(result.Discounts["p1"]) != 1 || result.PromoCode != nil {
		t.Fatal(result, err)
	}
}

func TestItemsCheckResponseFixtures(t *testing.T) {
	for _, test := range []struct {
		file    string
		code    string
		invalid bool
	}{
		{file: "valid", code: "SAVE"},
		{file: "invalid", code: "SAVE"},
		{file: "no_code"},
		{file: "null_code", code: "SAVE", invalid: true},
		{file: "null_message", code: "SAVE", invalid: true},
		{file: "missing_item", code: "SAVE", invalid: true},
		{file: "foreign_item", code: "SAVE", invalid: true},
		{file: "excess_discount", code: "SAVE", invalid: true},
	} {
		t.Run(test.file, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/items_check_" + test.file + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var response groupPricingJSON
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			result, err := response.pricing(ports.GroupPricingRequest{
				PromoCode: test.code,
				Items:     []ports.ItemRequest{{ProductID: "p1", Quantity: 3}},
			})
			if (err != nil) != test.invalid {
				t.Fatal(result, err)
			}
			if !test.invalid && (len(result.Items) != 1 || result.Items[0].Price.Kopecks() != 100) {
				t.Fatal(result)
			}
		})
	}
}
