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
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
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
		if errors.As(err, &shared) != test.shared || errs.CodeOf(err) != errs.Unavailable {
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
		sameCause := errors.Is(err, test.cause)
		if errors.As(err, &shared) != test.shared || !sameCause || errs.CodeOf(err) != errs.Unavailable {
			t.Fatal(test, err)
		}
	}
}

func TestOrderRefusalsHaveStableRetryClasses(t *testing.T) {
	for _, test := range []struct {
		status int
		body   string
		kind   errs.RetryKind
	}{
		{status: 409, body: `{"code":"OUT_OF_STOCK"}`, kind: errs.RetryPermanent},
		{status: 404, body: `{"code":"ORDER_NOT_FOUND"}`, kind: errs.RetryPermanent},
		{status: 503, body: `{"code":"MARKETPLACE_TEMPORARY_FAILURE"}`, kind: errs.RetryTransient},
		{status: 500, body: `{"code":"UNKNOWN"}`, kind: errs.RetryTransient},
		{status: 409, body: `{"code":"UNKNOWN"}`, kind: errs.RetryUnknown},
	} {
		response := httptest.NewRecorder()
		response.WriteHeader(test.status)
		_, _ = response.WriteString(test.body)
		reply := response.Result()
		err := refusal(http.MethodPost, "/orders", "t1", reply)
		_ = reply.Body.Close()
		if errs.RetryKindOf(err) != test.kind {
			t.Fatal(test, err)
		}
	}
}
