package platformapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestStubPaymentRecordedContract(t *testing.T) {
	for _, tc := range []struct {
		file    string
		status  int
		success bool
	}{
		{"stub_payment_paid.json", 200, true},
		{"stub_payment_amount.json", 409, false},
		{"stub_payment_state.json", 409, false},
		{"order_not_found.json", 404, false},
		{"order_shipped.json", 200, false},
		{"stub_payment_temporary.json", 503, false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			response, err := os.ReadFile("testdata/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := os.ReadFile("testdata/stub_payment_request.json")
			if err != nil {
				t.Fatal(err)
			}
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := "/api/v1/internal/marketplace/tenants/t1/orders/o1/stub-payment"
				if r.Method != http.MethodPost || r.URL.Path != path || r.Header.Get(HeaderToken) != "service-token" {
					t.Errorf("неверный запрос: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil || strings.TrimSpace(string(raw)) != strings.TrimSpace(string(expected)) {
					t.Errorf("тело: %s, %v", raw, err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write(response)
			})
			server := httptest.NewServer(handler)
			defer server.Close()
			client := New(Config{BaseURL: server.URL, Token: "service-token", Timeout: time.Second})
			for range 2 {
				err := client.StubPayment(context.Background(), "t1", "o1", money.FromKopecks(413000))
				if (err == nil) != tc.success {
					t.Fatal(err)
				}
				if tc.status >= http.StatusBadRequest {
					var refusal *ports.OrderRefusal
					if !errors.As(err, &refusal) || refusal.Unexpected ||
						!strings.Contains(string(response), refusal.Code) {
						t.Fatal("неизвестный отказ болванки", err)
					}
				}
			}
		})
	}
}

func TestStubPaymentMissingRoute(t *testing.T) {
	for _, body := range []string{"<html>Cannot POST /stub-payment</html>", `{"code":"ORDER_NOT_FOUND"}`} {
		reply := &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(body))}
		err := refusal(http.MethodPost, "/orders/o1/stub-payment", "t1", reply)
		if strings.HasPrefix(body, "<") {
			if !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
			continue
		}
		var got *ports.OrderRefusal
		if !errors.As(err, &got) || got.Code != "ORDER_NOT_FOUND" {
			t.Fatal(err)
		}
	}
}
