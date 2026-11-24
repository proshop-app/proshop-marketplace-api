package platformapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

type holdTransport func(*http.Request) (*http.Response, error)

func (f holdTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestPaymentHoldRecordedContract(t *testing.T) {
	for _, tc := range []struct {
		file   string
		status int
		code   string
		valid  bool
	}{
		{"payment_hold_waiting.json", 200, "", true},
		{"payment_hold_expired_repeat.json", 200, "", true},
		{"payment_hold_paid.json", 200, "", true},
		{"order_shipped.json", 200, "", true},
		{"payment_hold_missing.json", 200, "", false},
		{"payment_hold_wrong_order.json", 200, "", false},
		{"order_unknown_status.json", 200, "", false},
		{"order_cancelled.json", 200, "", false},
		{"payment_hold_expired.json", 409, "ORDER_PAYMENT_EXPIRED", false},
		{"payment_hold_cancelled.json", 409, "ORDER_NOT_AWAITING_PAYMENT", false},
		{"order_not_found.json", 404, "ORDER_NOT_FOUND", false},
		{"stub_payment_temporary.json", 503, "MARKETPLACE_TEMPORARY_FAILURE", false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			body, err := os.ReadFile("testdata/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			request, err := os.ReadFile("testdata/payment_hold_request.json")
			if err != nil {
				t.Fatal(err)
			}
			client := New(Config{BaseURL: "http://shop.test", Token: "token", Timeout: time.Second, Conns: 4})
			client.http.Transport = holdTransport(func(r *http.Request) (*http.Response, error) {
				path := "/api/v1/internal/marketplace/tenants/t1/orders/o1/payment-hold"
				if r.Method != http.MethodPost || r.URL.Path != path || r.Header.Get(HeaderToken) != "token" {
					t.Errorf("запрос: %s %s", r.Method, r.URL)
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil || strings.TrimSpace(string(raw)) != strings.TrimSpace(string(request)) {
					t.Errorf("тело запроса: %s, %v", raw, err)
				}
				return &http.Response{
					StatusCode: tc.status,
					Body:       io.NopCloser(strings.NewReader(string(body))),
					Header:     make(http.Header),
				}, nil
			})
			first, firstErr := client.PaymentHold(context.Background(), "t1", "o1")
			second, err := client.PaymentHold(context.Background(), "t1", "o1")
			if (firstErr == nil) != tc.valid || (err == nil) != tc.valid ||
				!first.PaymentHeldUntil.Equal(second.PaymentHeldUntil) {
				t.Fatal(first, second, firstErr, err)
			}
			if tc.code != "" {
				var refusal *ports.OrderRefusal
				if !errors.As(err, &refusal) || refusal.Code != tc.code || refusal.Unexpected {
					t.Fatal("неверный код отказа", err)
				}
			}
		})
	}
}
