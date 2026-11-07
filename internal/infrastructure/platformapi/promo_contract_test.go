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
	client := New(Config{BaseURL: "http://platform.test", Token: "service-token", Timeout: time.Second})
	client.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/internal/marketplace/pricing/contexts" {
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
