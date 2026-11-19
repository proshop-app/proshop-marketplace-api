package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestPublicCityUsesTrustedClientAddressAndPrivateResponse(t *testing.T) {
	source := &fake.Cities{Detection: ports.DetectedCity{City: "Город", Country: "RU"}}
	handler := &cityHandlers{service: appbuyer.NewCities(source, source, fake.NewLimiter()), trustedProxies: 1}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/city", nil)
	request.Header.Set("X-Forwarded-For", "untrusted, 192.0.2.8")
	response := httptest.NewRecorder()
	handler.detect(response, request)
	var body ports.DetectedCity
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 200 ||
		body.City != "Город" || response.Header().Get("Cache-Control") != "private, no-store" ||
		len(source.IPs) != 1 || source.IPs[0] != "192.0.2.8" {
		t.Fatal(response.Body, source.IPs, err)
	}
}
