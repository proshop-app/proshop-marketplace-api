package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	app "github.com/proshop-app/proshop-marketplace/internal/application/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func promotionHTTPFixture() (*promotionHandlers, *fake.Store, *fake.Clock) {
	store := fake.NewStore()
	clock := fake.NewClock(time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
	store.SeedTenant(participant.TenantState{TenantID: "t1", Slug: "t1", Status: participant.TenantActive, Version: 1})
	store.SeedParticipant(participant.State{
		TenantID:    "t1",
		Name:        "Магазин",
		Delivery:    participant.DeliverySettings{Regions: []string{"RU-AD"}, Carriers: []string{"cdek"}},
		Subsidy:     participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
		Enabled:     true,
		ShopEnabled: true,
		Status:      participant.StatusConnected,
		Version:     1,
	})
	store.SeedProduct(catalog.ProductState{
		TenantID:  "t1",
		ProductID: "p1",
		Card: catalog.Card{
			Name:        "Товар",
			Visible:     true,
			Marketplace: &taxonomy.Card{CategoryID: "c1", Published: true},
			Publication: taxonomy.Publication{CategoryID: "c1", Published: true},
		},
		PriceProfiles: []catalog.PriceProfile{{
			Key:            catalog.DefaultPriceProfile,
			Price:          money.FromKopecks(100),
			EffectivePrice: money.FromKopecks(100),
		}},
		Version: 1,
	})
	store.SeedVerdict(moderation.VerdictState{TenantID: "t1", ProductID: "p1", Status: moderation.Allowed, Version: 1})
	store.SeedPromotionPlacement(promotion.PlacementState{
		Code:         "category_slot",
		Positions:    []int{1, 5},
		Scope:        promotion.Category,
		SlotLimit:    1,
		DefaultPrice: money.FromKopecks(700),
		Active:       true,
		Version:      1,
	})
	service := app.NewService(store, clock, &fake.IDs{}, fake.NewIndex(), app.Config{
		Location:        time.UTC,
		Random:          fake.Random{},
		Limiter:         fake.NewLimiter(),
		EventsPerMinute: 2,
	})
	return &promotionHandlers{service: service}, store, clock
}

func promotionRequest(h http.HandlerFunc, method, path, body, tenant, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.SetPathValue("tenantId", tenant)
	req.SetPathValue("id", id)
	req.SetPathValue("code", "category_slot")
	result := httptest.NewRecorder()
	h(result, req)
	return result
}

func TestPromotionM12HTTPFlow(t *testing.T) {
	h, _, _ := promotionHTTPFixture()
	body := `{"placementCode":"category_slot","position":1,"offerId":"t1~p1","from":"2026-11-09","weeks":1,`
	body += `"marking":{"advertiser":"Рекламодатель","inn":"1234567890"}}`
	created := promotionRequest(h.create, "POST", "/", body, "t1", "")
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body.String())
	}
	var c campaignJSON
	if err := json.Unmarshal(created.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	id := string(c.ID)
	invalid := promotionRequest(h.edit, "PATCH", "/", `{"expectedVersion":1,"changes":{"unknown":1}}`, "t1", id)
	if invalid.Code != 400 {
		t.Fatal(invalid.Code, invalid.Body.String())
	}
	edited := promotionRequest(h.edit, "PATCH", "/", `{"expectedVersion":1,"changes":{"title":"Заявка"}}`, "t1", id)
	if edited.Code != 200 {
		t.Fatal(edited.Code, edited.Body.String())
	}
	submitted := promotionRequest(h.decide, "POST", "/submit", `{"expectedVersion":2}`, "t1", id)
	if submitted.Code != 200 {
		t.Fatal(submitted.Code, submitted.Body.String())
	}
	listed := promotionRequest(h.list, "GET", "/?productId=p1&limit=1", "", "t1", "")
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), `"review":1`) {
		t.Fatal(listed.Body.String())
	}
	missing := promotionRequest(h.list, "GET", "/?productId=p2", "", "t1", "")
	if missing.Code != 200 || !strings.Contains(missing.Body.String(), `"total":0`) {
		t.Fatal(missing.Body.String())
	}
	availability := promotionRequest(
		h.availability,
		"GET",
		"/?placementCode=category_slot&position=1&from=2026-11-09&weeks=1&categoryId=c1",
		"",
		"t1",
		"",
	)
	if availability.Code != 200 || !strings.Contains(availability.Body.String(), `"free":"none"`) {
		t.Fatal(availability.Code, availability.Body.String())
	}
	placements := promotionRequest(h.placements, "GET", "/", "", "t1", "")
	if placements.Code != 200 || !strings.Contains(placements.Body.String(), `"nextMonday":"2026-11-09"`) {
		t.Fatal(placements.Body.String())
	}
	config := `{"slotLimit":2,"defaultPriceKopecks":700,"active":true,"expectedVersion":1}`
	configured := promotionRequest(h.configure, "PUT", "/", config, "", "")
	if configured.Code != 200 {
		t.Fatal(configured.Code, configured.Body.String())
	}
	configured = promotionRequest(h.configure, "PUT", "/", config, "", "")
	if configured.Code != 409 {
		t.Fatal(configured.Code, configured.Body.String())
	}
	deleted := promotionRequest(h.delete, "DELETE", "/", "", "t1", id)
	if deleted.Code != 409 {
		t.Fatal(deleted.Code)
	}
	created = promotionRequest(h.create, "POST", "/", body, "t1", "")
	if err := json.Unmarshal(created.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	deleted = promotionRequest(h.delete, "DELETE", "/", "", "", string(c.ID))
	if deleted.Code != 204 {
		t.Fatal(deleted.Code, deleted.Body.String())
	}
}

func TestPromotionM12SessionEventsAndSponsored(t *testing.T) {
	h, store, clock := promotionHTTPFixture()
	id := ids.UUID("00000000-0000-0000-0000-000000000001")
	store.SeedPromotion(promotion.CampaignState{
		ID:            id,
		TenantID:      "t1",
		Status:        promotion.Active,
		PlacementCode: "category_slot",
		Position:      1,
		Target:        promotion.Target{OfferID: ids.OfferID{Tenant: "t1", Product: "p1"}, CategoryID: "c1"},
		Period:        promotion.Period{From: "2026-11-09", To: "2026-11-15"},
		Marking:       promotion.Marking{ERID: "erid"},
	})
	clock.Advance(72 * time.Hour)
	for i, visitor := range []string{"first", "second", "third"} {
		raw := `{"visitorId":"` + visitor + `","events":[{"promotionId":"` + string(id) + `","kind":"click"}]}`
		req := httptest.NewRequest("POST", "/", strings.NewReader(raw))
		req = req.WithContext(context.WithValue(req.Context(), keyBuyer, ids.UUID("buyer")))
		result := httptest.NewRecorder()
		h.events(result, req)
		want := 204
		if i == 2 {
			want = 429
		}
		if result.Code != want {
			t.Fatal(result.Code, result.Body.String())
		}
	}
	rows, err := h.service.Stats(context.Background(), app.Access{}, id, "2026-11-09", "2026-11-15")
	if err != nil || len(rows) != 1 || rows[0].Clicks != 1 {
		t.Fatal(rows, err)
	}
	sponsored := promotionRequest(h.sponsored, "GET", "/?category=c1", "", "", "")
	if sponsored.Code != 200 || !strings.Contains(sponsored.Body.String(), `"erid":"erid"`) {
		t.Fatal(sponsored.Code, sponsored.Body.String())
	}
	stats := promotionRequest(h.stats, "GET", "/", "", "t1", string(id))
	if stats.Code != 400 {
		t.Fatal(stats.Code, stats.Body.String())
	}
}
