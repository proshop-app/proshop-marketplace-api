package http_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
)

func TestPublicDeliverySettings(t *testing.T) {
	for _, percent := range []int{0, 30, 45, 100} {
		service := appcheckout.NewService(
			fake.NewStore(),
			nil,
			nil,
			nil,
			&fake.IDs{},
			fake.NewClock(t0),
			appcheckout.Config{DeliverySubsidyMinPercent: percent},
		)
		handler := transport.NewHandler(transport.Deps{
			Checkouts:  service,
			Registerer: prometheus.NewRegistry(),
			Logger:     slog.Default(),
		})
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/delivery/settings", nil)
		handler.ServeHTTP(response, request)
		var body struct {
			Percent int `json:"deliverySubsidyMinPercent"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || body.Percent != percent {
			t.Fatalf("публичный действующий порог %d: %d %s", percent, response.Code, response.Body.String())
		}
	}
}
