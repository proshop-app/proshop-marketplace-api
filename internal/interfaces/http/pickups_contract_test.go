package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
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
		"carrier=cdek&city=Город&bbox=3,2,1,4",
		"carrier=cdek&city=Город&limit=201",
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
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"total":0`) {
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

func TestCheckoutPickupPaginationAndOwnership(t *testing.T) {
	directory, platform, store := pickupHTTP(t)
	owner := ids.UUID("00000000-0000-4000-8000-000000000002")
	id := ids.UUID("00000000-0000-4000-8000-000000000001")
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	purchase, err := checkout.New(
		id,
		owner,
		"pickup-pages-key",
		[]checkout.Line{
			{
				Offer:    ids.OfferID{Tenant: "t1", Product: "p1"},
				Name:     "Товар",
				Quantity: 1,
			},
			{
				Offer:    ids.OfferID{Tenant: "t2", Product: "p1"},
				Name:     "Товар",
				Quantity: 1,
			},
		},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := purchase.SetDeliveryOptions(
		map[ids.TenantID][]string{"t1": {"cdek"}, "t2": {"cdek"}},
		map[ids.TenantID][]string{"t1": {"RU-AD"}, "t2": {"RU-MOS"}},
		now,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, purchase)
	}); err != nil {
		t.Fatal(err)
	}
	points := make([]delivery.PickupPoint, 5002)
	for i := range points {
		points[i] = delivery.PickupPoint{
			Carrier:    "cdek",
			Code:       strconv.Itoa(i),
			Name:       "Пункт",
			City:       "Город",
			Address:    "Адрес",
			RegionCode: "RU-AD",
			Location:   delivery.Location{Lat: 55, Lon: 37},
		}
	}
	outsideRegion := points[0]
	outsideRegion.Code = "outside-region"
	outsideRegion.RegionCode = "RU-MOS"
	platform.SetPickupPoints("t1", ports.PickupPoints{Available: true, Items: append(points, outsideRegion)})
	handler := &deliveryHandlers{
		directory: directory,
	}
	store.SeedTenant(participant.TenantState{TenantID: "t2", Status: participant.TenantActive})
	store.SeedParticipant(participant.State{
		TenantID: "t2",
		Stage:    participant.StageConnected,
		Delivery: participant.DeliverySettings{Carriers: []string{"cdek"}, Regions: []string{"RU-MOS"}},
	})
	for _, offset := range []int{0, 200} {
		response := httptest.NewRecorder()
		query := "/?carrier=cdek&city=Город&tenantId=t1&limit=200&offset=" + strconv.Itoa(offset)
		request := httptest.NewRequest(http.MethodGet, query, nil)
		request.SetPathValue("checkoutId", string(id))
		request = request.WithContext(context.WithValue(request.Context(), keyBuyer, owner))
		handler.checkoutPickupPoints(response, request)
		var body struct {
			Items []pickupPointJSON
			Total int
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK ||
			len(body.Items) != 200 || body.Total != len(points) || body.Items[0].Code != strconv.Itoa(offset) {
			t.Fatal(response.Code, body, err)
		}
	}
	for _, query := range []string{
		"/?carrier=cdek&city=Город&tenantId=t1&bbox=54,36,56,38",
		"/?carrier=cdek&city=Город&tenantId=t1&bbox=0,0,1,1",
		"/?carrier=cdek&city=Город&tenantId=t2&bbox=54,36,56,38",
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, query, nil)
		request.SetPathValue("checkoutId", string(id))
		request = request.WithContext(context.WithValue(request.Context(), keyBuyer, owner))
		handler.checkoutPickupPoints(response, request)
		var body struct {
			Items      []pickupPointJSON
			Total      int
			RefineArea bool
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK {
			t.Fatal(response.Code, response.Body, err)
		}
		switch {
		case strings.Contains(query, "tenantId=t2"):
			if body.Total != 1 || len(body.Items) != 1 || body.Items[0].RegionCode != "RU-MOS" || body.RefineArea {
				t.Fatal("пункты другого региона группы", body)
			}
		case strings.Contains(query, "bbox=54"):
			if body.Total != len(points) || len(body.Items) != delivery.PickupMapLimit || !body.RefineArea {
				t.Fatal(body.Total, len(body.Items), body.RefineArea)
			}
		case body.Total != 0 || len(body.Items) != 0:
			t.Fatal("точки вне bbox", body)
		}
	}
	if calls := platform.PickupCalls(); len(calls) != 1 || calls[0].Tenant != "t1" {
		t.Fatal("страницы должны переиспользовать справочник магазина покупки", calls)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?carrier=cdek&city=Город", nil)
	request.SetPathValue("checkoutId", string(id))
	request = request.WithContext(context.WithValue(request.Context(), keyBuyer, ids.UUID("another-buyer")))
	handler.checkoutPickupPoints(response, request)
	if response.Code != http.StatusNotFound || len(platform.PickupCalls()) != 1 {
		t.Fatal("чужая покупка не открывает выдачу", response.Code)
	}
}

func TestPickupQuotesRejectRepeatedTenantIDs(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodPost,
		"/",
		strings.NewReader(
			`{"carrier":"cdek","pickupPointCode":"P1","city":"Город","tenantIds":["t1","t1"]}`,
		),
	)
	request.SetPathValue("checkoutId", "00000000-0000-4000-8000-000000000001")
	response := httptest.NewRecorder()
	handler := &checkoutHandlers{}
	handler.quote(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatal(response.Code, response.Body)
	}
}

func TestPublicQuoteNeverDisclosesSellerCost(t *testing.T) {
	doc, _ := loadSpec(t)
	properties := doc.Components["schemas"]["Quote"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"cost", "subsidy"} {
		if _, found := properties[field]; found {
			t.Fatal("публичная схема раскрывает расход продавца", field)
		}
	}

	state := checkout.State{Groups: []checkout.Group{{
		Quote: &delivery.Quote{
			Status:  delivery.QuoteAvailable,
			Cost:    money.FromKopecks(30000),
			Subsidy: money.FromKopecks(30000),
			MinDays: 2,
			MaxDays: 4,
		},
	}}}
	body, err := toCheckoutJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, `"cost"`) || strings.Contains(text, `"subsidy"`) ||
		!strings.Contains(text, `"buyerCost":0.00`) || !strings.Contains(text, `"minDays":2`) {
		t.Fatal(text)
	}
}
