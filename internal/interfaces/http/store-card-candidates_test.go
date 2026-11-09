package http_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	app "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestStoreCardCandidatesHTTP(t *testing.T) {
	store := fake.NewStore()
	store.SeedParticipant(participant.State{
		TenantID: "t1",
		Slug:     "shop",
		Name:     "Магазин",
		Stage:    participant.StageConnected,
		Version:  1,
		Delivery: participant.DeliverySettings{Regions: []string{"RU-AD"}, Carriers: []string{"cdek"}},
		Subsidy:  participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
	})
	store.SeedTenant(participant.TenantState{TenantID: "t1", Status: participant.TenantActive, Version: 1})
	store.SeedProduct(catalog.ProductState{
		TenantID:  "t1",
		ProductID: "p1",
		Card:      catalog.Card{Name: "Сумка", Visible: true, Publication: taxonomy.Publication{Published: true}},
		PriceProfiles: []catalog.PriceProfile{{
			Key:            catalog.DefaultPriceProfile,
			Price:          money.FromKopecks(19999),
			EffectivePrice: money.FromKopecks(19999),
		}},
	})
	store.SeedVerdict(moderation.VerdictState{TenantID: "t1", ProductID: "p1", Status: moderation.Allowed})
	uppercase, _ := store.Product("t1", "p1")
	uppercase.ProductID = "ProductA"
	uppercase.Card.Name = "Другой товар"
	store.SeedProduct(uppercase)
	store.SeedVerdict(moderation.VerdictState{
		TenantID:  "t1",
		ProductID: "ProductA",
		Status:    moderation.Allowed,
	})
	service := app.NewService(store, fake.NewClock(t0), &fake.IDs{})
	handler := transport.NewHandler(transport.Deps{
		Registerer:     prometheus.NewRegistry(),
		Logger:         slog.Default(),
		Participants:   service,
		OperatorToken:  token,
		ServerAPIToken: "server-token",
	})
	path := "/api/v1/internal/tenants/t1/store-card/products"
	call := func(query, bearer string, want int) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path+query, nil)
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("%s: %d %s", query, response.Code, response.Body.String())
		}
		return response
	}
	for _, bearer := range []string{"", "wrong", token} {
		call("", bearer, http.StatusUnauthorized)
	}
	response := call("?query=%D0%A1%D0%A3%D0%9C%D0%9A%D0%90&page=1&limit=10", "server-token", http.StatusOK)
	var page app.CardCandidates
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	body := response.Body.String()
	for _, forbidden := range []string{`"price"`, `"fromPrice"`, `"tenantId"`} {
		if strings.Contains(body, forbidden) {
			t.Fatal(body)
		}
	}
	if page.Items[0].OfferID != "t1~p1" || page.Items[0].Image != nil {
		t.Fatal(page)
	}
	call("?ids=p1", "server-token", http.StatusOK)
	uppercaseResponse := call("?ids=ProductA", "server-token", http.StatusOK)
	if !strings.Contains(uppercaseResponse.Body.String(), `"productId":"ProductA"`) {
		t.Fatal(uppercaseResponse.Body.String())
	}
	for _, query := range []string{
		"?tenantId=other",
		"?page=0",
		"?limit=21",
		"?page=1&page=2",
		"?ids=",
		"?ids=p1,p1",
		"?ids=p1,p2,p3,p4",
		"?query=search&ids=p1",
		"?ids[$ne]=p1",
	} {
		call(query, "server-token", http.StatusBadRequest)
	}
	store.SeedVerdict(moderation.VerdictState{TenantID: "t1", ProductID: "p1", Status: moderation.Rejected})
	response = call("?ids=p1", "server-token", http.StatusOK)
	if !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatal(response.Body.String())
	}
}
