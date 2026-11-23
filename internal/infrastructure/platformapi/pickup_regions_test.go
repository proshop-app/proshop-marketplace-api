package platformapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

// Тот же ответ настоящих адаптеров, который проверяется в server marketplace-official-pickups.test.ts:
// СДЭК https://api.edu.cdek.ru/v2/deliverypoints?city_code=44&type=PVZ&size=1&page=0 (18.11.2026);
// Яндекс https://yandex.ru/support/delivery-profile/ru/api/other-day/ref/2.-Tochki-samoprivoza-i-PVZ/
// apib2bplatformpickup-pointslist-post. Сырые официальные записи и происхождение — в fixtures/official сервера.
func TestOfficialPickupRegionsSurviveHTTPAndRespectShopArea(t *testing.T) {
	data, err := os.ReadFile("testdata/official_pickups.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[delivery.Carrier]json.RawMessage
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, carrier := range []delivery.Carrier{"cdek", "yandex"} {
		t.Run(string(carrier), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Query().Get("city") != "Москва" {
					_, _ = w.Write([]byte(`{"available":true,"total":0,"nextCursor":null,"items":[]}`))
					return
				}
				_, _ = w.Write(fixtures[delivery.Carrier(r.URL.Query().Get("carrier"))])
			}))
			defer server.Close()
			store := fake.NewStore()
			store.SeedTenant(participant.TenantState{TenantID: "t1", Status: participant.TenantActive})
			store.SeedParticipant(participant.State{
				TenantID: "t1",
				Stage:    participant.StageConnected,
				Enabled:  true,
				Delivery: participant.DeliverySettings{
					Regions:  []string{"RU-MOW"},
					Carriers: []string{"cdek", "yandex"},
				},
			})
			client := New(Config{BaseURL: server.URL, Timeout: time.Second, Conns: 4})
			service := appdelivery.NewService(store, client, fake.NewPickupCache(), appdelivery.Config{
				Attempts:     1,
				QuoteTimeout: time.Second,
				TTL:          time.Hour,
			})
			query := func(regions []string, bounds *delivery.Bounds) (delivery.PickupArea, error) {
				return service.QueryFor(context.Background(), carrier, "Москва", regions, bounds, 0, 100)
			}
			allowed, err := query([]string{"RU-MOW"}, nil)
			if err != nil || allowed.Total != 1 || len(allowed.Items) != 1 || allowed.Items[0].RegionCode != "RU-MOW" {
				t.Fatal(allowed, err)
			}
			for _, regions := range [][]string{{"RU-TA"}, {}} {
				if _, err := query(regions, nil); !errors.Is(err, delivery.ErrRegionNotAllowed) {
					t.Fatal("неподходящая область магазина должна иметь явную причину", regions, err)
				}
			}
			emptyArea, err := query([]string{"RU-MOW"}, &delivery.Bounds{0, 0, 1, 1})
			if err != nil || emptyArea.Total != 0 {
				t.Fatal("пустая область карты — не запрет региона магазина", emptyArea, err)
			}
			if calls.Load() != 1 {
				t.Fatal("фильтр регионов не должен обращаться к магазину", calls.Load())
			}
			emptyCity, err := service.Query(context.Background(), appdelivery.PickupQuery{
				Carrier: carrier,
				City:    "Город без ПВЗ",
				Limit:   100,
			})
			if err != nil || emptyCity.Total != 0 {
				t.Fatal("действительно пустой город остаётся успешным", emptyCity, err)
			}
		})
	}
}

func TestUnmappedPickupRegionIsSourceFailureAndNeverAnEmptySuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"MARKETPLACE_PICKUP_REGION_UNMAPPED","message":"region unmapped"}`))
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL, Timeout: time.Second, Conns: 4})
	_, err := client.PickupPoints(context.Background(), "t1", "cdek", "Москва")
	var failure *ports.PickupCatalogFailure
	if !errors.As(err, &failure) {
		t.Fatal("ошибка справочника не должна превращаться в пустой город", err)
	}
}
