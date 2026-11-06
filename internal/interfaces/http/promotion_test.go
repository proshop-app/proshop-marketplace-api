package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	app "github.com/proshop-app/proshop-marketplace/internal/application/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
)

func TestPromotionMachineTokenAndTenant(t *testing.T) {
	store := fake.NewStore()
	id := "00000000-0000-0000-0000-000000000001"
	store.SeedPromotion(promotion.CampaignState{
		ID:       "00000000-0000-0000-0000-000000000001",
		TenantID: "t1",
		Version:  1,
		Status:   promotion.Draft,
	})
	service := app.NewService(store, fake.NewClock(time.Now()), &fake.IDs{}, fake.NewIndex(), app.Config{
		Location: time.UTC,
		Random:   fake.PromotionRandom{},
	})
	h := &promotionHandlers{service: service}
	for _, test := range []struct {
		token  string
		header string
		tenant string
		want   int
	}{
		{"", "Bearer secret", "t1", 404},
		{"secret", "", "t1", 401},
		{"secret", "Bearer wrong", "t1", 401},
		{"secret", "Bearer secret", "t2", 404},
		{"secret", "Bearer secret", "t1", 200},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.SetPathValue("id", id)
		req.SetPathValue("tenantId", test.tenant)
		req.Header.Set("Authorization", test.header)
		recorder := httptest.NewRecorder()
		serverAPI(test.token, h.campaign)(recorder, req)
		if recorder.Code != test.want {
			t.Fatal(recorder.Code, recorder.Body.String())
		}
	}
}

func TestPromotionPauseAndBatch(t *testing.T) {
	store := fake.NewStore()
	store.SeedPromotion(promotion.CampaignState{
		ID:       "00000000-0000-0000-0000-000000000001",
		TenantID: "t1",
		Status:   promotion.Review,
		Version:  1,
	})
	service := app.NewService(
		store,
		fake.NewClock(time.Now()),
		&fake.IDs{},
		fake.NewIndex(),
		app.Config{Location: time.UTC},
	)
	h := &promotionHandlers{service: service}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"action":"pause","expectedVersion":1}`))
	req.SetPathValue("id", "00000000-0000-0000-0000-000000000001")
	recorder := httptest.NewRecorder()
	h.decide(recorder, req)
	if recorder.Code != 400 || !strings.Contains(recorder.Body.String(), "promotion.action_unsupported") {
		t.Fatal(recorder.Code, recorder.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"visitorId":"","events":[]}`))
	recorder = httptest.NewRecorder()
	h.events(recorder, req)
	if recorder.Code != 400 {
		t.Fatal(recorder.Code)
	}
}
