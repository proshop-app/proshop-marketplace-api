package searchapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/searchapi"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestFacetSelectionWireContract51(t *testing.T) {
	empty := []ports.FacetCode{}
	var nilCodes []ports.FacetCode
	partial := []ports.FacetCode{ports.FacetCategory, ports.FacetTenant}
	for _, tc := range []struct {
		name   string
		facets *[]ports.FacetCode
		wire   string
	}{
		{
			name: "прежний запрос без нового поля",
		},
		{
			name:   "явно пустой список",
			facets: &empty,
			wire:   `[]`,
		},
		{
			name:   "пустой селектор с nil slice",
			facets: &nilCodes,
			wire:   `[]`,
		},
		{
			name:   "частичный список",
			facets: &partial,
			wire:   `["category","tenant"]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bodies := make(chan []byte, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				bodies <- body
				_, _ = w.Write([]byte(`{"total":0,"items":[],"facets":[]}`))
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
			for _, contexts := range []map[ids.TenantID]string{
				nil,
				{"t1": "marketplace:default"},
			} {
				_, err := client.Search(context.Background(), ports.OfferQuery{
					Facets:        tc.facets,
					PriceContexts: contexts,
				})
				if err != nil {
					t.Fatal(err)
				}
				var request map[string]json.RawMessage
				if err := json.Unmarshal(<-bodies, &request); err != nil {
					t.Fatal(err)
				}
				if string(request["facets"]) != tc.wire {
					t.Fatalf("facets=%s, ожидалось %q", request["facets"], tc.wire)
				}
				if string(request["facetFields"]) != "[]" {
					t.Fatalf("пустые атрибутные фасеты: %s", request["facetFields"])
				}
				want := "null"
				if contexts != nil {
					want = `{"t1":"marketplace:default"}`
				}
				if string(request["priceContexts"]) != want {
					t.Fatalf("ценовая область изменена: %s, ожидалось %s", request["priceContexts"], want)
				}
			}
		})
	}
}
