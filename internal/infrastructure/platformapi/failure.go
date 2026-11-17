package platformapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func transportFailure(err error) error {
	failure := ErrUnavailable.Wrap(err)
	if errors.Is(err, context.Canceled) {
		return failure
	}
	var connection *net.OpError
	disconnected := errors.As(err, &connection) && connection.Op == "dial"
	if disconnected || errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return &ports.PlatformServerFailure{Cause: failure}
	}
	return failure
}

func gatewayFailure(reply *http.Response) bool {
	switch reply.StatusCode {
	case http.StatusBadGateway, http.StatusGatewayTimeout:
		return true
	case http.StatusServiceUnavailable:
		// 503 приложения с кодом по договору — сбой магазина, а не общего шлюза.
		raw, err := io.ReadAll(io.LimitReader(reply.Body, (4<<20)+1))
		reply.Body = io.NopCloser(bytes.NewReader(raw))
		var body errorJSON
		application := err == nil && len(raw) <= 4<<20 && json.Unmarshal(raw, &body) == nil &&
			body.Code != ""
		return !application
	default:
		return false
	}
}

func responseRefusal(
	method, path string,
	tenant ids.TenantID,
	reply *http.Response,
	parse func(string, string, ids.TenantID, *http.Response) error,
) error {
	// Копия сохраняет исходный Body для Close в вызывающем методе.
	replay := *reply
	shared := gatewayFailure(&replay)
	failure := parse(method, path, tenant, &replay)
	if shared {
		return &ports.PlatformServerFailure{Cause: failure}
	}
	return failure
}
