package searchapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/searchapi"
)

func TestAdapterBatchesAndPreservesIntegerPrice(t *testing.T) {
	token := strings.Repeat("m", 32)
	price := int64(9007199254740993)
	var batches []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Admin-Id") != token {
			t.Error("не передан служебный токен")
		}

		if r.URL.Path == "/api/v1/internal/marketplace/search" {
			_, _ = w.Write([]byte(`{"total":1,"items":[{"tenantId":"t1","productId":"p1",
				"price":9007199254740993,"basePrice":9007199254740993,"version":4}]}`))
			return
		}
		var body struct {
			Offers []struct {
				Price   int64 `json:"price"`
				Version int64 `json:"version"`
			} `json:"offers"`
		}

		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		batches = append(batches, len(body.Offers))

		for _, offer := range body.Offers {
			if offer.Price != price || offer.Version != 4 {
				t.Errorf("предложение в запросе: %+v", offer)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := searchapi.New(searchapi.Config{
		URL:     server.URL,
		Token:   token,
		Timeout: time.Second,
	})

	if err != nil {
		t.Fatal(err)
	}
	docs := make([]ports.OfferDocument, 401)

	for position := range docs {
		docs[position] = ports.OfferDocument{
			Price:   price,
			Version: 4,
		}
	}

	if err := client.Put(context.Background(), docs); err != nil {
		t.Fatal(err)
	}

	if len(batches) != 3 || batches[0] != 200 || batches[1] != 200 || batches[2] != 1 {
		t.Fatalf("границы пачек: %v", batches)
	}
	page, err := client.Search(context.Background(), ports.OfferQuery{
		Limit: 10,
	})

	if err != nil || len(page.Items) != 1 || page.Items[0].Price != price || page.Items[0].ID != "t1~p1" {
		t.Fatalf("предложение в ответе: %+v, %v", page, err)
	}
}

func TestAdapterDoesNotFollowRedirectOrAcceptUnconfirmedWrite(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "/credential-leak")
				w.WriteHeader(status)
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
			err = client.Put(context.Background(), []ports.OfferDocument{{
				ID: "t1~p1",
			}})

			if !errors.Is(err, searchapi.ErrUnavailable) || calls != 1 {
				t.Fatalf("небезопасный повтор или успех: calls=%d err=%v", calls, err)
			}
		})
	}
}
