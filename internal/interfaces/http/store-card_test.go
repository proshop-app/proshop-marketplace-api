package http_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	app "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestStoreCardHTTP(t *testing.T) {
	store := fake.NewStore()
	logo := "logo-key"
	state := participant.State{
		TenantID: "t1",
		Slug:     "shop",
		Name:     "Магазин",
		Stage:    participant.StageConnected,
		Version:  1,
		Delivery: participant.DeliverySettings{Regions: []string{"RU-AD"}, Carriers: []string{"cdek"}},
		Subsidy:  participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
		StoreCard: participant.StoreCard{
			Readiness: participant.CardReadiness{Ready: true},
			Profile:   participant.Profile{Logo: &logo},
		},
	}
	store.SeedParticipant(state)
	store.SeedTenant(participant.TenantState{
		TenantID: "t1",
		Slug:     "shop",
		Status:   participant.TenantActive,
		Version:  1,
	})
	store.SeedProduct(catalog.ProductState{
		TenantID:  "t1",
		ProductID: "p1",
		Card:      catalog.Card{Name: "Товар", Visible: true, Publication: taxonomy.Publication{Published: true}},
		PriceProfiles: []catalog.PriceProfile{{
			Key:            catalog.DefaultPriceProfile,
			Price:          money.FromKopecks(100),
			EffectivePrice: money.FromKopecks(100),
		}},
	})
	store.SeedVerdict(moderation.VerdictState{TenantID: "t1", ProductID: "p1", Status: moderation.Allowed})
	service := app.NewService(store, fake.NewClock(t0), &fake.IDs{})
	refresh := func() {
		t.Helper()
		task := ports.Task{Kind: ports.TaskStoreCard, TenantID: "t1"}
		if err := service.RefreshStoreCard(context.Background(), task); err != nil {
			t.Fatal(err)
		}
	}
	refresh()
	handler := transport.NewHandler(transport.Deps{
		Features:       appbuyer.NewFeatures(store, nil, fake.NewPlatform(), fake.NewLimiter()),
		Registerer:     prometheus.NewRegistry(),
		Logger:         slog.Default(),
		Participants:   service,
		OperatorToken:  token,
		ServerAPIToken: "server-token",
	})
	call := func(method, path, body, bearer string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
		}
		return response
	}
	sync := "/api/v1/internal/tenants/t1/settings/sync"
	summary := "/api/v1/internal/tenants/t1/store-card"
	for _, bearer := range []string{"", token, "wrong"} {
		call("POST", sync, "", bearer, http.StatusUnauthorized)
		call("GET", summary, "", bearer, http.StatusUnauthorized)
	}
	call("POST", sync, "", "server-token", http.StatusAccepted)
	call("POST", sync, "", "server-token", http.StatusAccepted)
	if rows := store.OutboxRows(); len(rows) != 1 || rows[0].Kind != app.TaskSyncSettings {
		t.Fatal(rows)
	}
	responseSummary := call("GET", summary, "", "server-token", http.StatusOK)
	var summaryBody map[string]json.RawMessage
	if err := json.Unmarshal(responseSummary.Body.Bytes(), &summaryBody); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for key := range summaryBody {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	wantKeys := strings.Fields("badge categories fromPrice hidden offers rank rating showcase uncategorized")
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatal(keys)
	}
	// Имя задаёт магазин в сверке: ручки переименования участника нет.
	call("PATCH", "/api/v1/operator/participants/t1", `{"name":"Другое имя"}`, token, http.StatusNotFound)
	hide := "/api/v1/operator/participants/t1/card/hide"
	call("POST", hide, `{"key":"name","reason":"Причина"}`, token, http.StatusBadRequest)
	call("DELETE", hide+"/name", "", token, http.StatusBadRequest)
	hiddenResponse := call("POST", hide, `{"key":"logo","reason":"Причина"}`, token, http.StatusOK)
	var member struct {
		Card struct {
			Logo   *string                  `json:"logo"`
			Hidden []participant.CardHidden `json:"hidden"`
		} `json:"card"`
	}
	if err := json.Unmarshal(hiddenResponse.Body.Bytes(), &member); err != nil {
		t.Fatal(err)
	}
	if member.Card.Logo == nil || *member.Card.Logo != logo {
		t.Fatal(member)
	}
	if len(member.Card.Hidden) != 1 || member.Card.Hidden[0].Reason != "Причина" {
		t.Fatal(member)
	}
	refresh()
	for _, path := range []string{"/api/v1/shops", "/api/v1/shops/shop"} {
		response := call("GET", path, "", "", http.StatusOK)
		body := response.Body.String()
		if !strings.Contains(body, `"logo":null`) {
			t.Fatal(body)
		}
		if response.Header().Get("Cache-Control") != "public, max-age=0, must-revalidate" {
			t.Fatal(response.Header())
		}
		cached := httptest.NewRequest(http.MethodGet, path, nil)
		cached.Header.Set("If-None-Match", response.Header().Get("ETag"))
		cachedResponse := httptest.NewRecorder()
		handler.ServeHTTP(cachedResponse, cached)
		if cachedResponse.Code != http.StatusNotModified {
			t.Fatal(cachedResponse.Code)
		}

	}
	call("DELETE", hide+"/logo", "", token, http.StatusOK)
	refresh()
	response := call("GET", "/api/v1/shops/shop", "", "", http.StatusOK)
	if !strings.Contains(response.Body.String(), `"logo":"logo-key"`) {
		t.Fatal(response.Body.String())
	}
	var header map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &header); err != nil {
		t.Fatal(err)
	}
	keys = keys[:0]
	for key := range header {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	headerKeys := "categories color initial isNew logo name offers rating showcase slug subscribers tenantId"
	wantKeys = strings.Fields(headerKeys)
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatal(keys)
	}
	var showcase []map[string]json.RawMessage
	if err := json.Unmarshal(header["showcase"], &showcase); err != nil {
		t.Fatal(err)
	}
	if len(showcase) != 1 {
		t.Fatal(showcase)
	}
	if len(showcase[0]) != 2 || showcase[0]["offerId"] == nil || showcase[0]["image"] == nil {
		t.Fatal(showcase)
	}
	state, _ = store.Participant("t1")
	state.StoreCard.Readiness.Ready = false
	store.SeedParticipant(state)
	refresh()
	response = call("GET", "/api/v1/shops", "", "", http.StatusOK)
	if !strings.Contains(response.Body.String(), `"items":[]`) {
		t.Fatal(response.Body.String())
	}
	response = call("GET", "/api/v1/shops/shop", "", "", http.StatusOK)
	if !strings.Contains(response.Body.String(), `"logo":"logo-key"`) {
		t.Fatal(response.Body.String())
	}
	call("GET", summary, "", "server-token", http.StatusOK)
	state.Enabled = false
	state.Stage = participant.StageCandidate
	store.SeedParticipant(state)
	refresh()
	call("GET", "/api/v1/shops/shop", "", "", http.StatusNotFound)
	call("GET", summary, "", "server-token", http.StatusNotFound)
}
