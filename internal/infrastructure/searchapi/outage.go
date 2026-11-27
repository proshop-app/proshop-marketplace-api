package searchapi

import (
	"context"
	"net/http"
	"time"
)

const outageProbeTimeout = 3 * time.Second

// ConfirmOutage проверяет общий путь записи после сбоя индексации.
func (x *Index) ConfirmOutage(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, outageProbeTimeout)
	defer cancel()
	status, err := x.request(ctx, http.MethodGet, "/write-availability", nil, nil)
	switch status {
	case 0:
		// Истечение срока пробы может означать ожидание соединения в локальном пуле.
		return err != nil && ctx.Err() == nil
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}
