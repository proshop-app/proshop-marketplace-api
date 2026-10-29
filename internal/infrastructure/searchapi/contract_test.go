package searchapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/searchapi"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestContractPreservesPricesAttributesDeliveryAndFacets(t *testing.T) {
	ctx := context.Background()
	price := int64(9007199254740993)
	var saved json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/internal/marketplace/offers" {
			var batch struct {
				Offers []json.RawMessage `json:"offers"`
			}

			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || len(batch.Offers) != 1 {
				t.Errorf("пачка предложений: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			saved = batch.Offers[0]
			w.WriteHeader(http.StatusNoContent)
			return
		}

		var query struct {
			PriceContexts map[string]string                `json:"priceContexts"`
			Attributes    map[string]ports.AttributeFilter `json:"attributes"`
			InStock       bool                             `json:"inStock"`
			InStockSet    bool                             `json:"inStockSet"`
			FacetFields   []struct {
				Code string `json:"code"`
				Type string `json:"type"`
			} `json:"facetFields"`
		}

		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Error(err)
		}

		if query.PriceContexts["t1"] != "marketplace:group" || !query.InStockSet || query.InStock ||
			query.Attributes["a2"].Type != "number" || query.Attributes["a2"].Min != "1.5" ||
			len(query.FacetFields) != 1 || query.FacetFields[0].Code != "a2" {
			t.Errorf("контекст поиска потерян: %+v", query)
		}

		response := struct {
			Items  []json.RawMessage `json:"items"`
			Total  int               `json:"total"`
			Facets []ports.Facet     `json:"facets"`
		}{
			Items: []json.RawMessage{saved},
			Total: 1,
			Facets: []ports.Facet{
				{
					Code:  "price",
					Count: 1,
				},
			},
		}

		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	client, err := searchapi.New(searchapi.Config{
		URL:     server.URL,
		Token:   strings.Repeat("m", 32),
		Timeout: time.Second,
	})

	if err != nil {
		t.Fatal(err)
	}
	doc := ports.OfferDocument{
		ID:               "t1~p1",
		TenantID:         "t1",
		ProductID:        "p1",
		CategoryID:       "c2",
		CategoryPath:     []string{"c1", "c2"},
		Price:            price,
		FreeDeliveryFrom: price,
		Delivery: participant.DeliverySettings{
			Regions:  []string{},
			Carriers: []string{"cdek"},
		},
		CheckoutAvailable: true,
		Attributes: map[string]json.RawMessage{
			"a1": json.RawMessage(`"001"`),
			"a2": json.RawMessage(`1.75`),
			"a3": json.RawMessage(`false`),
			"a4": json.RawMessage(`"true"`),
		},
		PriceProfiles: []catalog.PriceProfile{
			{
				Key:            catalog.DefaultPriceProfile,
				Price:          money.FromKopecks(price),
				EffectivePrice: money.FromKopecks(price),
			},
			{
				Key:            "marketplace:group",
				Price:          money.FromKopecks(0),
				EffectivePrice: money.FromKopecks(0),
			},
		},
	}

	if err := client.Put(ctx, []ports.OfferDocument{doc}); err != nil {
		t.Fatal(err)
	}
	page, err := client.Search(ctx, ports.OfferQuery{
		PriceContexts: map[ids.TenantID]string{
			"t1": "marketplace:group",
		},
		InStockSet: true,
		Attributes: map[string]ports.AttributeFilter{
			"a2": {
				Type: "number",
				Min:  "1.5",
			},
		},
		FacetFields: []taxonomy.Attribute{
			{
				Code: "a2",
				Type: "number",
			},
		},
	})

	if err != nil || len(page.Items) != 1 || len(page.Facets) != 1 {
		t.Fatalf("ответ каталога: %+v, %v", page, err)
	}
	got := page.Items[0]

	if got.Price != price || got.FreeDeliveryFrom != price || !reflect.DeepEqual(got.Attributes, doc.Attributes) ||
		!reflect.DeepEqual(got.PriceProfiles, doc.PriceProfiles) || !reflect.DeepEqual(got.Delivery, doc.Delivery) ||
		!reflect.DeepEqual(got.CategoryPath, doc.CategoryPath) {
		t.Fatalf("изменились данные после HTTP-передачи: %+v", got)
	}
}

func TestOldSearchContractIsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ready":true}`))
	}))
	defer server.Close()

	client, err := searchapi.New(searchapi.Config{
		URL:     server.URL,
		Token:   strings.Repeat("m", 32),
		Timeout: time.Second,
	})

	if err != nil {
		t.Fatal(err)
	}

	if ready, err := client.Exists(context.Background()); ready || !errors.Is(err, searchapi.ErrUnavailable) {
		t.Fatalf("старый поиск принят как совместимый: ready=%v, err=%v", ready, err)
	}
}
