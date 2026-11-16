package platformapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

func TestCancellationRecordedContract(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		status  int
		create  bool
	}{
		{"order_cancelled.json", 200, false},
		{"order_cancel_absent.json", 200, false},
		{"order_not_cancellable.json", 409, false},
		{"order_checkout_cancelled.json", 409, true},
		{"order_cancel_unavailable.json", 503, false},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/" + tc.fixture)
			if err != nil {
				t.Fatal(err)
			}
			client := New(Config{BaseURL: "http://platform.test", Token: "service-token", Timeout: time.Second})
			client.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
				path := "/api/v1/internal/marketplace/tenants/t1/orders"
				if !tc.create {
					path += "/cancel-by-checkout"
				}
				if r.Method != http.MethodPost || r.URL.Path != path || r.Header.Get(HeaderToken) != "service-token" {
					t.Error(r.URL, r.Header)
				}
				if !tc.create {
					var body, want ports.CancelOrderRequest
					request, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					expected, err := os.ReadFile("testdata/order_cancel_request.json")
					if err != nil {
						t.Fatal(err)
					}
					if json.Unmarshal(request, &body) != nil || json.Unmarshal(expected, &want) != nil || body != want {
						t.Fatal(string(request), string(expected))
					}
				}
				w := httptest.NewRecorder()
				w.WriteHeader(tc.status)
				_, _ = w.Write(raw)
				return w.Result(), nil
			})
			var result ports.CancelOrderResult
			if tc.create {
				_, err = client.CreateOrder(context.Background(), "t1", ports.CreateOrderRequest{})
			} else {
				result, err = client.CancelOrder(context.Background(), "t1", ports.CancelOrderRequest{
					ExternalCheckoutID: "checkout1:t1",
					GroupID:            "t1",
					Reason:             "creation_timeout",
				})
			}
			switch tc.fixture {
			case "order_cancelled.json":
				if err != nil || result.Absent || result.OrderID != "o1" {
					t.Fatal(result, err)
				}
			case "order_cancel_absent.json":
				if err != nil || !result.Absent || result.OrderID != "" {
					t.Fatal(result, err)
				}
			case "order_not_cancellable.json":
				var conflict *ports.OrderNotCancellable
				if !errors.As(err, &conflict) || conflict.OrderID != "o1" || conflict.Status != checkout.GroupPaid {
					t.Fatal(err)
				}
			default:
				var refusal *ports.OrderRefusal
				if !errors.As(err, &refusal) || refusal.Unexpected {
					t.Fatal(err)
				}
				code := "MARKETPLACE_TEMPORARY_FAILURE"
				if tc.create {
					code = "ORDER_CHECKOUT_CANCELLED"
				}
				if refusal.Code != code {
					t.Fatal(refusal)
				}
			}
		})
	}
}

func TestCancellationRejectsMalformedSuccess(t *testing.T) {
	for _, raw := range []string{
		`{"status":"absent"}`,
		`{"status":"absent","orderId":"o1"}`,
		`{"status":"cancelled","orderId":null}`,
		`{"status":"paid","orderId":"o1"}`,
	} {
		client := New(Config{BaseURL: "http://platform.test"})
		client.http.Transport = reviewTransport(func(*http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			_, _ = w.WriteString(raw)
			return w.Result(), nil
		})
		_, err := client.CancelOrder(context.Background(), "t1", ports.CancelOrderRequest{})
		if !errors.Is(err, ErrInvalidResponse) || !errors.Is(err, ports.ErrInvalidCancellationResponse) {
			t.Fatal(raw, err)
		}
	}
}
