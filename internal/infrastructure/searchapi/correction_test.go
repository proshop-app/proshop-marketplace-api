package searchapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestSearchCorrectionContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/search_corrections.json")
	if err != nil {
		t.Fatal(err)
	}
	var responses map[string]json.RawMessage
	if err := json.Unmarshal(raw, &responses); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"normal", "typo", "layout", "translit", "partial", "unknown", "empty"} {
		t.Run(name, func(t *testing.T) {
			client, err := New(Config{
				URL:     "http://search.test",
				Token:   strings.Repeat("m", 32),
				Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			client.client.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != apiPath+"/search" {
					t.Error("неожиданный маршрут", r.URL.Path)
				}
				response := httptest.NewRecorder()
				_, _ = response.Write(responses[name])
				return response.Result(), nil
			})
			page, err := client.Search(context.Background(), ports.OfferQuery{
				Text:  "товра",
				Limit: 20,
			})
			if name == "unknown" || name == "empty" {
				if !errors.Is(err, ErrUnavailable) {
					t.Fatalf("некорректное исправление принято: %+v, %v", page, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Price != 19999 ||
				page.Items[0].BasePrice != 29999 || page.Items[0].ID != "t1~p1" {
				t.Fatal("изменён контракт предложения", page)
			}
			if name == "normal" {
				if page.Correction != nil {
					t.Fatal("обычная выдача содержит исправление", page.Correction)
				}
				return
			}
			if page.Correction == nil || page.Correction.Query != "товар" || page.Correction.Kind != name {
				t.Fatal("потеряно исправление поиска", page.Correction)
			}
		})
	}
}
