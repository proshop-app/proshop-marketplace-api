package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func sellersHandler(t *testing.T, key string) (catalogHandlers, *fake.SellersCounts) {
	t.Helper()
	member := participant.State{
		TenantID: "t1",
		Stage:    participant.StageConnected,
		Enabled:  true,
		Delivery: participant.DeliverySettings{Carriers: []string{"cdek", "yandex"}, Regions: []string{"RU-MOW"}},
	}
	tenant := participant.TenantState{TenantID: "t1", Status: participant.TenantActive}
	product := catalog.ProductState{
		TenantID:  "t1",
		ProductID: "p1",
		Card: catalog.Card{
			Visible:     true,
			ModelKey:    key,
			Publication: taxonomy.Publication{Published: true},
		},
		PriceProfiles: []catalog.PriceProfile{{Key: catalog.DefaultPriceProfile}},
	}
	state := ports.OfferState{
		Product:     catalog.RestoreProduct(product),
		Participant: participant.Restore(member),
		Tenant:      participant.RestoreTenant(tenant),
		Verdict:     moderation.RestoreVerdict(moderation.VerdictState{Status: moderation.Allowed}),
	}
	doc, _ := appcatalog.Document(state)
	index := &fake.PromotionIndex{
		Index:  fake.NewIndex(),
		Result: ports.OfferPage{Items: []ports.OfferDocument{doc}, Total: 1},
	}
	if err := index.Put(context.Background(), []ports.OfferDocument{doc}); err != nil {
		t.Fatal(err)
	}
	store := fake.NewStore()
	store.SeedParticipant(member)
	store.SeedTenant(tenant)
	counter := &fake.SellersCounts{States: []ports.OfferState{state}}
	return catalogHandlers{
		queries: appcatalog.NewQueries(store, index, fake.NewPlatform(), nil, nil, nil),
		sellers: counter,
	}, counter
}

func TestSellersCountHTTP(t *testing.T) {
	for _, path := range []string{"/offers", "/offers?ids=t1~p1", "/offers/t1~p1"} {
		for _, mode := range []string{"success", "failure", "unknown", "zero", "missing"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				key := "model"
				if mode == "unknown" {
					key = ""
				}
				handler, counter := sellersHandler(t, key)
				if mode == "failure" {
					counter.Err = errors.New("база недоступна")
				}
				if mode == "zero" {
					counter.Counts = map[string]int{"model": 0}
				}
				if mode == "missing" {
					counter.Counts = map[string]int{}
				}
				var logs bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&logs, nil)).With("requestId", "request-test")
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request = request.WithContext(context.WithValue(request.Context(), keyLogger, logger))
				response := httptest.NewRecorder()
				detail := strings.HasSuffix(path, "/t1~p1")
				if detail {
					request.SetPathValue("offerId", "t1~p1")
					handler.card(response, request)
				} else {
					handler.list(response, request)
				}
				if response.Code != http.StatusOK {
					t.Fatal(response.Code, response.Body.String())
				}
				var body map[string]json.RawMessage
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if !detail {
					var items []map[string]json.RawMessage
					if err := json.Unmarshal(body["items"], &items); err != nil || len(items) != 1 {
						t.Fatal(err, response.Body.String())
					}
					body = items[0]
				}
				if mode == "success" && string(body["sellersCount"]) != "1" {
					t.Fatal(body)
				}
				if mode != "success" && body["sellersCount"] != nil || body["modelKey"] != nil {
					t.Fatal(body)
				}
				wantCalls := 1
				if mode == "unknown" {
					wantCalls = 0
				}
				if counter.Calls != wantCalls {
					t.Fatal(counter.Calls)
				}
				if mode == "failure" && !strings.Contains(logs.String(), `"requestId":"request-test"`) {
					t.Fatal(logs.String())
				}
			})
		}
	}
}

func TestSellersCancellationIsNotLogged(t *testing.T) {
	handler, counter := sellersHandler(t, "model")
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	ctx = context.WithValue(ctx, keyLogger, slog.New(slog.NewJSONHandler(&logs, nil)))
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/offers", nil).WithContext(ctx)
	cards := []appcatalog.OfferCard{{Offer: ports.OfferDocument{ModelKey: "model"}}}
	if handler.populateSellers(request, cards) || logs.Len() != 0 || counter.Calls != 0 {
		t.Fatal(logs.String(), counter.Calls)
	}
}

func TestOpenAPISellersCount(t *testing.T) {
	doc, _ := loadSpec(t)
	offer := doc.Components["schemas"]["Offer"].(map[string]any)
	properties := offer["properties"].(map[string]any)
	count := properties["sellersCount"].(map[string]any)
	if count["minimum"] != 1 {
		t.Fatal(count)
	}
	if count["type"] != "integer" || count["description"] == nil || properties["modelKey"] != nil {
		t.Fatal(properties)
	}
	for _, field := range offer["required"].([]any) {
		if field == "sellersCount" {
			t.Fatal("счётчик должен быть необязательным")
		}
	}
}
