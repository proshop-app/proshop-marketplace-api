package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestSeparateVariantRequestAndCardReference(t *testing.T) {
	store, index := fake.NewStore(), fake.NewIndex()
	// Порядок схемы намеренно отличается: ручка вариантов им больше не пользуется.
	store.SeedTaxonomy(taxonomy.Definition{
		Attributes: []taxonomy.Attribute{
			{
				Code:   "size",
				Name:   "Размер",
				Type:   "enum",
				Values: []string{"S", "M"},
			},
		},
		Schemas: map[string]taxonomy.Schema{
			"schema": {
				Attributes: []string{"size"},
			},
		},
		Categories: []taxonomy.Category{
			{
				ID:     "category",
				Slug:   "category",
				Name:   "Категория",
				Schema: "schema",
			},
		},
	})
	doc := ports.OfferDocument{
		ID:         "seller~m",
		ProductID:  "m",
		TenantID:   "seller",
		CategoryID: "category",
		Visible:    true,
		VariantsID: "group",
		Variants: &catalog.Variants{
			GroupID: "group",
			Axes: []catalog.VariantAxis{
				{
					Key:   "platform.size",
					Code:  "size",
					Name:  "Размер",
					Value: "M",
				},
			},
		},
		Price:     money.FromKopecks(12000).Kopecks(),
		BasePrice: money.FromKopecks(12345).Kopecks(),
	}
	small := doc
	small.ID, small.ProductID = "seller~s", "s"
	small.Variants = &catalog.Variants{
		GroupID:  "group",
		Position: 1,
		Axes: []catalog.VariantAxis{
			{
				Key:   "platform.size",
				Code:  "size",
				Name:  "Размер",
				Value: "S",
			},
		},
	}
	if err := index.Put(context.Background(), []ports.OfferDocument{doc, small}); err != nil {
		t.Fatal(err)
	}
	handler := catalogHandlers{
		queries: appcatalog.NewQueries(store, index, fake.NewPlatform(), nil, nil, nil),
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/variants/seller/group", nil)
	request.SetPathValue("tenantId", "seller")
	request.SetPathValue("groupId", "group")
	response := httptest.NewRecorder()
	handler.variants(response, request)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{
		`"options":[{"key":"platform.size","code":"size","name":"Размер","values":["M","S"]}]`,
		`"values":["M","S"]`,
		`"price":120`,
		`"basePrice":123.45`,
		`"attrs":{"platform.size":"M"}`,
		`"id":"seller~s"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("нет %s: %s", expected, body)
		}
	}
	card, err := json.Marshal(toOfferJSON(appcatalog.OfferCard{Offer: doc}, true))
	if err != nil || !strings.Contains(string(card), `"variants":{"groupId":"group"}`) {
		t.Fatalf("карточка содержит только ссылку: %s %v", card, err)
	}
	if !publicMoneyRoute(http.MethodGet, "/api/v1/catalog/variants/{tenantId}/{groupId}") {
		t.Fatal("маршрут обязан соблюдать денежный контракт")
	}
}
