package searchapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
)

type featureTransport func(*http.Request) (*http.Response, error)

func (f featureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPriceBucketsAndRatingContract(t *testing.T) {
	for _, name := range []string{"search_buckets.json", "search_no_buckets.json"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/" + name)
			if err != nil {
				t.Fatal(err)
			}
			transport := featureTransport(func(r *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				var query map[string]any
				if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
					t.Error(err)
				}
				if _, legacy := query["tenantId"]; legacy {
					t.Error("передан старый tenantId")
				}
				tenants, ok := query["tenantIds"].([]any)
				if !ok || len(tenants) != 2 || tenants[0] != "t1" || tenants[1] != "t2" {
					t.Error(query)
				}
				if query["sort"] != "rating" {
					t.Error(query)
				}
				_, _ = w.Write(raw)
				return w.Result(), nil
			})
			client, err := New(Config{
				URL:     "http://search.test",
				Token:   strings.Repeat("m", 32),
				Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			client.client.Transport = transport
			page, err := client.Search(context.Background(), ports.OfferQuery{
				TenantIDs: []string{"t1", "t2"},
				Sort:      ports.SortRating,
				Limit:     20,
			})
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != 1 || len(page.Facets) != 1 || page.Facets[0].Count != 5 {
				t.Fatal(page)
			}
			buckets := page.Facets[0].Buckets
			if name == "search_buckets.json" {
				if len(buckets) != 2 || buckets[1].From != 10000 || buckets[1].To != 20000 || buckets[1].Count != 3 {
					t.Fatal(buckets)
				}
				rating := page.Items[0].Rating
				if rating == nil || rating.Value != 4.5 || rating.Count != 12 {
					t.Fatal(page.Items)
				}
			} else if len(buckets) != 0 || len(page.Items) != 1 || page.Items[0].Rating != nil {
				t.Fatal(buckets)
			}
		})
	}
}

func TestVersionFiveDocumentContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/search_buckets.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Items []offerDocument `json:"items"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	doc, err := fromDocument(fixture.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{
		URL:     "http://search.test",
		Token:   strings.Repeat("m", 32),
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.client.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		if strings.HasSuffix(r.URL.Path, "/status") {
			_, _ = w.Write([]byte(`{"contractVersion":5,"ready":true}`))
			return w.Result(), nil
		}
		var body struct {
			Offers []json.RawMessage `json:"offers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Offers) != 1 {
			t.Error(body)
			return w.Result(), nil
		}
		var sent offerDocument
		if err := json.Unmarshal(body.Offers[0], &sent); err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body.Offers[0], &fields); err != nil {
			t.Fatal(err)
		}
		if _, exists := fields["salesCount"]; exists {
			t.Error("salesCount передан поиску")
		}
		if !slices.Equal(sent.Assets, []string{"p/main.jpg", "p/detail.jpg"}) {
			t.Error(sent.Assets)
		}
		if sent.Rating == nil || *sent.Rating != *fixture.Items[0].Rating {
			t.Error(sent)
		}
		w.WriteHeader(http.StatusNoContent)
		return w.Result(), nil
	})
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Put(context.Background(), []ports.OfferDocument{doc}); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentNormalizesRating(t *testing.T) {
	for _, value := range []float64{-1, 4.333, 6} {
		doc, err := toDocument(ports.OfferDocument{Rating: &catalog.Rating{Value: value, Count: 2}})
		if err != nil || doc.Rating == nil || doc.Rating.Value < 0 || doc.Rating.Value > 5 {
			t.Fatal(doc, err)
		}
	}
	if _, err := toDocument(ports.OfferDocument{Rating: &catalog.Rating{Value: 4, Count: -1}}); err == nil {
		t.Fatal("отрицательный count отправлен в поиск")
	}
}

func TestStatusVersionFourRejected(t *testing.T) {
	client, err := New(Config{
		URL:     "http://search.test",
		Token:   strings.Repeat("m", 32),
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.client.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/status") {
			t.Error("ожидался /status", r.URL.Path)
		}
		w := httptest.NewRecorder()
		_, _ = w.Write([]byte(`{"contractVersion":4,"ready":true}`))
		return w.Result(), nil
	})
	if err := client.Ping(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("контракт 3 принят", err)
	}
}

func TestEmptyAssetsAreArray(t *testing.T) {
	doc, err := toDocument(ports.OfferDocument{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(doc)
	if err != nil || !strings.Contains(string(raw), `"assets":[]`) {
		t.Fatalf("%s: %v", raw, err)
	}
}
