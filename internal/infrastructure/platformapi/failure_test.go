package platformapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestGatewayAndApplicationFailures(t *testing.T) {
	for _, test := range []struct {
		status int
		body   string
		shared bool
	}{
		{status: 502, body: "gateway", shared: true},
		{status: 503, body: "gateway", shared: true},
		{status: 504, body: "gateway", shared: true},
		{status: 503, body: `{"code":"MARKETPLACE_TEMPORARY_FAILURE"}`},
		{status: 503, body: `{"code":"MARKETPLACE_STATUS_UNAVAILABLE"}`},
		{status: 503, body: `{"code":"UNEXPECTED_TENANT_FAILURE"}`},
		{status: 500, body: `{"code":"TENANT_FAILURE"}`},
	} {
		client := New(Config{BaseURL: "http://platform.test"})
		client.http.Transport = reviewTransport(func(*http.Request) (*http.Response, error) {
			reply := httptest.NewRecorder()
			reply.WriteHeader(test.status)
			_, _ = reply.WriteString(test.body)
			return reply.Result(), nil
		})
		_, err := client.MarketplaceSettings(context.Background(), "t1")
		var shared *ports.PlatformServerFailure
		if errors.As(err, &shared) != test.shared {
			t.Fatal(test, err)
		}
		if test.status == 503 && strings.Contains(test.body, "TEMPORARY") {
			var refusal *ports.OrderRefusal
			if !errors.As(err, &refusal) || refusal.Code != "MARKETPLACE_TEMPORARY_FAILURE" {
				t.Fatal("потерян договор отказа", err)
			}
		}
	}
}

func TestConnectionFailuresDifferFromTimeoutAndCancellation(t *testing.T) {
	for _, test := range []struct {
		cause  error
		shared bool
	}{
		{cause: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, shared: true},
		{cause: context.DeadlineExceeded},
		{cause: context.Canceled},
	} {
		err := transportFailure(test.cause)
		var shared *ports.PlatformServerFailure
		if errors.As(err, &shared) != test.shared || !errors.Is(err, test.cause) {
			t.Fatal(test, err)
		}
	}
}
