package http

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/searchapi"
)

type searchListingRequest struct {
	FacetKeys      []string `json:"facetKeys"`
	PriceHistogram bool     `json:"priceHistogram"`
	Offset         int      `json:"offset"`
	Limit          int      `json:"limit"`
}

func TestCatalogListingContractThroughSearchClient(t *testing.T) {
	requests := make(chan searchListingRequest, 4)
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/internal/marketplace/search" {
			t.Errorf("запрос к поиску: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var query searchListingRequest
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- query
		w.Header().Set("Content-Type", "application/json")
		if len(query.FacetKeys) > 0 {
			_, _ = io.WriteString(w, `{"items":[],"total":10000,"totalRelation":"gte","limited":true,"facets":[
				{"code":"price","type":"number","min":100,"max":200,"buckets":[{"from":100,"to":200,"count":25}]},
				{"code":"attr.color","type":"enum","values":[{"value":"red","count":25}]}
			]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"items":[{"id":"shop~p2400","tenantId":"shop","productId":"p2400"}],
			"total":10000,"totalRelation":"gte","limited":true,"facets":[]}`)
	}))
	t.Cleanup(engine.Close)
	index, err := searchapi.New(searchapi.Config{
		URL:   engine.URL,
		Token: strings.Repeat("x", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	store := fake.NewStore()
	store.SeedTaxonomy(taxonomy.Definition{
		Schemas: map[string]taxonomy.Schema{
			"s1": {Attributes: []string{"color"}},
		},
		Categories: []taxonomy.Category{{ID: "c1", Schema: "s1"}},
		Attributes: []taxonomy.Attribute{{Code: "color", Name: "Цвет", Type: "enum", Facet: true}},
	})
	handler := catalogHandlers{queries: appcatalog.NewQueries(store, index, nil, nil, nil, nil)}
	request := func(query string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.list(response, httptest.NewRequest(http.MethodGet, "/api/v1/catalog/offers?"+query, nil))
		return response
	}
	readPage := func(response *httptest.ResponseRecorder) offerPageJSON {
		t.Helper()
		var page offerPageJSON
		err := json.Unmarshal(response.Body.Bytes(), &page)
		if response.Code != http.StatusOK || err != nil || page.Total != 10000 ||
			page.TotalRelation != "gte" || !page.Limited {
			t.Fatalf("ответ выдачи: HTTP %d %s (%v)", response.Code, response.Body, err)
		}
		return page
	}
	assertQuery := func(want searchListingRequest) {
		t.Helper()
		select {
		case got := <-requests:
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("запрос движка: %+v, ожидался %+v", got, want)
			}
		default:
			t.Fatal("движок не получил запрос")
		}
	}
	last := readPage(request("offset=2399&limit=20"))
	if len(last.Items) != 1 || last.Offset+len(last.Items) != appcatalog.MaxWindow || last.Limit != 1 {
		t.Fatalf("последняя порция: %+v", last)
	}
	assertQuery(searchListingRequest{Offset: 2399, Limit: 1})
	stop := request("offset=2400")
	if stop.Code != http.StatusBadRequest || !strings.Contains(stop.Body.String(), "уточните фильтры") ||
		len(requests) != 0 {
		t.Fatalf("за пределами окна: HTTP %d %s, запросов к движку %d", stop.Code, stop.Body, len(requests))
	}
	facets := readPage(request("category=c1&facetKeys=attr.color&facetKeys=price&priceHistogram=true"))
	if len(facets.Items) != 0 || len(facets.Facets) != 2 {
		t.Fatalf("догрузка фасетов: %+v", facets)
	}
	assertQuery(searchListingRequest{
		FacetKeys:      []string{"attr.color", "price"},
		PriceHistogram: true,
		Limit:          20,
	})
}
