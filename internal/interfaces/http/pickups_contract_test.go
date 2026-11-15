package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func pickupHTTP(t *testing.T) (*appdelivery.Service, *fake.Platform, *fake.Store) {
	t.Helper()
	store := fake.NewStore()
	platform := fake.NewPlatform()
	store.SeedTenant(participant.TenantState{TenantID: "t1", Status: participant.TenantActive})
	store.SeedParticipant(participant.State{
		TenantID: "t1",
		Stage:    participant.StageConnected,
		Enabled:  true,
		Subsidy:  participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
		Delivery: participant.DeliverySettings{Regions: []string{"RU-AD"}, Carriers: []string{"cdek"}},
	})
	platform.SetPickupPoints("t1", ports.PickupPoints{
		Available: true,
		Items: []delivery.PickupPoint{
			{
				Carrier:    "cdek",
				Code:       "p1",
				Name:       "Пункт",
				City:       "Город",
				Address:    "Адрес",
				RegionCode: "RU-AD",
				Location:   delivery.Location{Lat: 55, Lon: 37},
			},
		},
	})
	service := appdelivery.NewService(store, platform, fake.NewPickupCache(), appdelivery.Config{
		Attempts:     3,
		TTL:          time.Hour,
		QuoteTimeout: time.Second,
	})
	return service, platform, store
}

func TestPickupPublicContract(t *testing.T) {
	service, platform, _ := pickupHTTP(t)
	handler := &deliveryHandlers{directory: service}
	for _, query := range []string{
		"city=Город",
		"carrier=CDEK&city=Город",
		"carrier=cdek",
		"carrier=cdek&city=" + strings.Repeat("а", 121),
		"carrier=cdek&city=Город&bbox=1,2,3,4",
		"carrier=cdek&city=Город&limit=1",
	} {
		response := httptest.NewRecorder()
		handler.pickupPoints(response, httptest.NewRequest(http.MethodGet, "/?"+query, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatal(query, response.Code, response.Body)
		}
	}
	response := httptest.NewRecorder()
	handler.pickupPoints(response, httptest.NewRequest(http.MethodGet, "/?carrier=cdek&city=Город", nil))
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 200 || len(body.Items) != 1 {
		t.Fatal(response.Code, response.Body, err)
	}
	point := body.Items[0]
	if len(point) != 8 || point["code"] != "p1" || point["regionCode"] != "RU-AD" || point["schedule"] != "" {
		t.Fatal(point)
	}
	platform.Fail("t1", errors.New("нет связи"))
	response = httptest.NewRecorder()
	handler.pickupPoints(response, httptest.NewRequest(http.MethodGet, "/?carrier=cdek&city=Другой", nil))
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), "seller_unreachable") {
		t.Fatal(response.Code, response.Body)
	}
	response = httptest.NewRecorder()
	handler.pickupPoints(response, httptest.NewRequest(http.MethodGet, "/?carrier=yandex&city=Город", nil))
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"items":[]}` {
		t.Fatal(response.Code, response.Body)
	}
}

func TestProfilePickupContract(t *testing.T) {
	service, platform, store := pickupHTTP(t)
	features := appbuyer.NewFeatures(store, nil, platform, fake.NewLimiter(), service)
	handler := &buyerFeatureHandlers{service: features}
	for _, test := range []struct {
		body   string
		status int
		code   string
	}{
		{`{"pickupPoint":{"provider":"cdek","code":"p1"}}`, 400, "delivery.invalid_city"},
		{`{"pickupPoint":{"provider":"cdek","code":"p1","city":"Город"}}`, 200, "Адрес"},
		{`{"pickupPoint":{"provider":"cdek","code":"p1"}}`, 200, "Адрес"},
		{`{"pickupPoint":{"provider":"cdek","code":"p1","city":""}}`, 200, "Адрес"},
		{`{"pickupPoint":{"provider":"cdek","code":"changed"}}`, 400, "delivery.invalid_city"},
		{`{"pickupPoint":{"provider":"yandex","code":"p1"}}`, 400, "delivery.invalid_city"},
		{`{"pickupPoint":{"provider":"cdek","code":"unknown","city":"Город"}}`, 404, "delivery.pickup_point_not_found"},
	} {
		request := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(test.body))
		request = request.WithContext(context.WithValue(request.Context(), keyBuyer, ids.UUID("buyer")))
		response := httptest.NewRecorder()
		handler.patchProfile(response, request)
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) {
			t.Fatal(response.Code, response.Body)
		}
	}
	_, err := features.Patch(context.Background(), "buyer", func(state buyer.ProfileState) (buyer.ProfileState, error) {
		state.PickupPoint.Code = "unknown"
		return state, nil
	})
	if !errors.Is(err, delivery.ErrPickupPointNotFound) {
		t.Fatal("изменение вложенного указателя обошло проверку", err)
	}
	profile, err := features.Profile(context.Background(), "buyer")
	if err != nil || profile.PickupPoint == nil || profile.PickupPoint.Code != "p1" ||
		profile.PickupPoint.City != "Город" {
		t.Fatal(profile, err)
	}
}

func TestQuoteRequiresCity(t *testing.T) {
	handler := &checkoutHandlers{}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"carrier":"cdek","pickupPointCode":"p1"}`))
	request.SetPathValue("checkoutId", "00000000-0000-4000-8000-000000000001")
	response := httptest.NewRecorder()
	handler.quote(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "delivery.invalid_city") {
		t.Fatal(response.Code, response.Body)
	}
}
