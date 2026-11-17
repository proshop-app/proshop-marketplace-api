package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestOfferIDsValidation(t *testing.T) {
	h := catalogHandlers{}
	cases := []string{
		"ids=",
		"ids=invalid",
		"ids=t1~p1&ids=invalid",
		strings.Repeat("ids=t1~p1&", 21),
	}
	for _, key := range []string{
		"q",
		"tenant",
		"groupId",
		"category",
		"priceMin",
		"priceMax",
		"inStock",
		"sort",
		"limit",
		"offset",
		"track",
		"attr.a1",
		"attr.a1.min",
	} {
		cases = append(cases, "ids=t1~p1&"+key+"=")
	}
	for _, query := range cases {
		t.Run(query, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.list(w, httptest.NewRequest(http.MethodGet, "/offers?"+query, nil))
			if w.Code != 400 || !strings.Contains(w.Body.String(), "catalog.invalid_query") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestOfferIDsResponse(t *testing.T) {
	store, index, platform := fake.NewStore(), fake.NewIndex(), fake.NewPlatform()
	store.SeedTenant(participant.TenantState{TenantID: "t1", Status: participant.TenantActive})
	store.SeedParticipant(participant.State{
		TenantID: "t1",
		Enabled:  true,
		Stage:    participant.StageConnected,
		Delivery: participant.DeliverySettings{
			Carriers: []string{"cdek"},
			Regions:  []string{"RU-MOW"},
		},
	})
	platform.SetBuyerProfile("buyer", "t1", "personal")
	for _, product := range []string{"p1", "p2"} {
		doc := ports.OfferDocument{
			ID:          "t1~" + product,
			TenantID:    "t1",
			ProductID:   product,
			Visible:     true,
			Listed:      true,
			Description: "Описание",
			Assets:      []string{"image"},
			Delivery: participant.DeliverySettings{
				Carriers: []string{"cdek"},
				Regions:  []string{"RU-MOW"},
			},
			PriceProfiles: []catalog.PriceProfile{
				{
					Key:            catalog.DefaultPriceProfile,
					Price:          money.FromKopecks(10000),
					EffectivePrice: money.FromKopecks(10000),
				},
				{
					Key:            "personal",
					Price:          money.FromKopecks(9000),
					EffectivePrice: money.FromKopecks(8000),
				},
			},
		}
		if err := index.Put(context.Background(), []ports.OfferDocument{doc}); err != nil {
			t.Fatal(err)
		}
	}
	h := catalogHandlers{queries: appcatalog.NewQueries(store, index, platform, nil, nil, nil)}
	for _, buyer := range []ids.UUID{"", "buyer"} {
		query := "/offers?ids=t1~p2&ids=t1~p1&ids=t1~p2&ids=t1~missing&deliveryCarrier=cdek&deliveryRegion=RU-MOW"
		r := httptest.NewRequest(http.MethodGet, query, nil)
		r = r.WithContext(context.WithValue(r.Context(), keyBuyer, buyer))
		w := httptest.NewRecorder()
		h.list(w, r)
		var page offerPageJSON
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 {
			t.Fatal(w.Code, w.Body.String(), err)
		}
		if page.Total != 2 || page.Limit != 3 || page.Offset != 0 || len(page.Items) != 2 ||
			page.Facets == nil || len(page.Facets) != 0 {
			t.Fatal(page)
		}
		if page.Items[0].ID != "t1~p2" || page.Items[1].ID != "t1~p1" {
			t.Fatal(page.Items)
		}
		wantPrice := rubles(10000)
		if buyer != "" {
			wantPrice = 8000
		}
		for _, item := range page.Items {
			if item.Price != wantPrice || item.DeliveryAvailable == nil || !*item.DeliveryAvailable {
				t.Fatal(item)
			}
		}
		for _, field := range []string{"description", "assets", "variants"} {
			if strings.Contains(w.Body.String(), `"`+field+`"`) {
				t.Fatal(w.Body.String())
			}
		}
	}
	w := httptest.NewRecorder()
	h.list(w, httptest.NewRequest(http.MethodGet, "/offers?ids=t1~missing", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
	index.BeforeGet = func(context.Context, string) error {
		return errors.New("индекс недоступен")
	}
	w = httptest.NewRecorder()
	h.list(w, httptest.NewRequest(http.MethodGet, "/offers?ids=t1~p1", nil))
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestOpenAPIOfferIDs(t *testing.T) {
	doc, _ := loadSpec(t)
	operation := doc.Paths["/api/v1/catalog/offers"]["get"].(map[string]any)
	for _, raw := range operation["parameters"].([]any) {
		parameter := raw.(map[string]any)
		if parameter["name"] != "ids" {
			continue
		}
		schema := parameter["schema"].(map[string]any)
		if parameter["in"] != "query" || parameter["style"] != "form" || parameter["explode"] != true ||
			schema["type"] != "array" || schema["minItems"] != 1 || schema["maxItems"] != 20 {
			t.Fatal(parameter)
		}
		if operation["responses"].(map[string]any)["503"] == nil {
			t.Fatal("нет ответа 503")
		}
		return
	}
	t.Fatal("нет параметра ids")
}
