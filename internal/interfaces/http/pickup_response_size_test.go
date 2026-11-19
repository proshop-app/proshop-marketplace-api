package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func fixturePoints(n int) []delivery.PickupPoint {
	points := make([]delivery.PickupPoint, n)
	for i := range points {
		points[i] = delivery.PickupPoint{
			Carrier:    "cdek",
			Code:       "MSK" + strconv.Itoa(100000+i),
			Name:       "Пункт выдачи заказов СДЭК на улице Примерной",
			City:       "Город",
			Address:    "г. Город, ул. Примерная, д. " + strconv.Itoa(i) + ", помещение 1",
			RegionCode: "RU-AD",
			Schedule:   "Пн-Пт 10:00-21:00, Сб-Вс 10:00-18:00",
			Location:   delivery.Location{Lat: 55 + float64(i%300)*0.003, Lon: 37 + float64(i/300)*0.003},
		}
	}
	return points
}

// Г: размер и время ответа публичной ручки и карты при 20 000 пунктах города.
func TestPickupResponsesRespectAreaAndPageLimits(t *testing.T) {
	service, platform, store := pickupHTTP(t)
	points := fixturePoints(20000)
	platform.SetPickupPoints("t1", ports.PickupPoints{Available: true, Items: points})
	handler := &deliveryHandlers{directory: service}

	start := time.Now()
	response := httptest.NewRecorder()
	handler.pickupPoints(response, httptest.NewRequest(http.MethodGet, "/?carrier=cdek&city=Город", nil))
	t.Logf(
		"публичная GET /delivery/pickup-points (холодно): %d, %d байт, %s",
		response.Code,
		response.Body.Len(),
		time.Since(start),
	)
	var page struct {
		Items []pickupPointJSON
		Total int
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(page.Items) != 100 || page.Total != 20000 || response.Body.Len() > 100000 {
		t.Fatal("нет предела страницы", response.Code, len(page.Items), page.Total, response.Body.Len())
	}
	start = time.Now()
	response = httptest.NewRecorder()
	handler.pickupPoints(response, httptest.NewRequest(http.MethodGet, "/?carrier=cdek&city=Город", nil))
	t.Logf(
		"публичная GET /delivery/pickup-points (тепло): %d, %d байт, %s",
		response.Code,
		response.Body.Len(),
		time.Since(start),
	)

	owner := ids.UUID("00000000-0000-4000-8000-000000000002")
	id := ids.UUID("00000000-0000-4000-8000-000000000001")
	now := time.Now()
	purchase, err := checkout.New(
		id,
		owner,
		"pickup-size-key",
		[]checkout.Line{{
			Offer:    ids.OfferID{Tenant: "t1", Product: "p1"},
			Name:     "Товар",
			Quantity: 1,
		}},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := purchase.SetDeliveryOptions(
		map[ids.TenantID][]string{"t1": {"cdek"}},
		map[ids.TenantID][]string{"t1": {"RU-AD"}},
		now,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Do(
		context.Background(),
		func(ctx context.Context, tx ports.Tx) error {
			return tx.Checkouts().Add(ctx, purchase)
		},
	); err != nil {
		t.Fatal(err)
	}
	store.SeedParticipant(participant.State{
		TenantID: "t1",
		Stage:    participant.StageConnected,
		Enabled:  true,
		Delivery: participant.DeliverySettings{Regions: []string{"RU-AD"}, Carriers: []string{"cdek", "yandex"}},
	})
	scoped := &deliveryHandlers{
		directory: service,
	}
	for _, query := range []string{
		"/?carrier=cdek&city=Город&bbox=-90,-180,90,180",
		"/?carrier=cdek&city=Город&bbox=55,37,55.05,37.05",
		"/?carrier=cdek&city=Город&bbox=56,38,55,37",
		"/?carrier=cdek&city=Город&bbox=-91,0,0,0",
		"/?carrier=cdek&city=Город&limit=1000",
		"/?carrier=cdek&city=Город&offset=100000000",
		"/?carrier=cdek&city=Город",
	} {
		start := time.Now()
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, query, nil)
		request.SetPathValue("checkoutId", string(id))
		request = request.WithContext(context.WithValue(request.Context(), keyBuyer, owner))
		scoped.checkoutPickupPoints(response, request)
		if response.Code == http.StatusOK {
			var page struct {
				Items      []pickupPointJSON
				Total      int
				RefineArea bool
			}
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Items) > delivery.PickupMapLimit {
				t.Fatal("ответ превышает предел карты", len(page.Items))
			}
			if query == "/?carrier=cdek&city=Город&bbox=-90,-180,90,180" && (!page.RefineArea || page.Total != 20000) {
				t.Fatal("большая область должна требовать уточнения", page)
			}
		}
		body := response.Body.String()
		if len(body) > 160 {
			body = body[len(body)-160:]
		}
		t.Logf("карта %s: %d, %d байт, %s …%s", query, response.Code, response.Body.Len(), time.Since(start), body)
	}
}
