package searchapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const (
	wireReceipt  = "03af0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab"
	wireBuyerKey = "e414f05f65a0bfe8e784db84e0678c60035d13dfa7b0e65f4a1f31f250cbb5b2"
)

func TestSearchObservationWire(t *testing.T) {
	pageJSON := compactFeedbackFixture(t, "observed-page.json")
	client := feedbackSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertFeedbackRequest(t, r, "/search", strings.Repeat("s", 32))
		var query offerQuery
		if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
			t.Error(err)
		}
		if !query.Observe || !query.TrackQuery || query.BuyerKey != wireBuyerKey || query.Text != "кроссовки" {
			t.Errorf("поиск: %+v", query)
		}
		_, _ = w.Write(pageJSON)
	})
	page, err := client.Search(context.Background(), ports.OfferQuery{
		Text:       "кроссовки",
		Observe:    true,
		TrackQuery: true,
		BuyerKey:   wireBuyerKey,
		Limit:      20,
	})
	if err != nil || page.Receipt != wireReceipt || len(page.Items) != 1 || page.Items[0].ID != "seller-a~p42" {
		t.Fatal(page, err)
	}
}

func TestSearchRetriesBadObservationOnceWithoutBuyerData(t *testing.T) {
	for _, outcome := range []struct {
		name           string
		observe        bool
		secondRejected bool
		wantCalls      int32
	}{
		{name: "выдача восстановлена", observe: true, wantCalls: 2},
		{name: "повтор также отклонён", observe: true, secondRejected: true, wantCalls: 2},
		{name: "обычный запрос без повтора", wantCalls: 1},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			var calls atomic.Int32
			client := feedbackSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				first := calls.Add(1) == 1
				if first && outcome.observe {
					if string(body["observe"]) != "true" || string(body["buyerKey"]) != `"`+wireBuyerKey+`"` {
						t.Errorf("отсутствуют сведения для наблюдения: %v", body)
					}
				} else if body["observe"] != nil || body["buyerKey"] != nil {
					t.Error("повтор отправил данные наблюдения")
				}
				if string(body["trackQuery"]) != "true" || string(body["text"]) != `"кроссовки"` {
					t.Error("потеряны параметры текстового запроса")
				}
				if first || outcome.secondRejected {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_, _ = io.WriteString(w, `{"total":1,"items":[]}`)
			})
			query := ports.OfferQuery{
				Text:       "кроссовки",
				Observe:    outcome.observe,
				TrackQuery: true,
			}
			if outcome.observe {
				query.BuyerKey = wireBuyerKey
			}
			page, err := client.Search(context.Background(), query)
			if calls.Load() != outcome.wantCalls {
				t.Fatalf("запросов %d, ожидалось %d", calls.Load(), outcome.wantCalls)
			}
			if !outcome.observe || outcome.secondRejected {
				if !errors.Is(err, ErrInvalid) {
					t.Fatal(err)
				}
			} else if err != nil || page.Total != 1 || page.Receipt != "" {
				t.Fatal(page, err)
			}
		})
	}
}

func TestSearchFeedbackWire(t *testing.T) {
	for _, item := range []struct {
		file   string
		event  ports.SearchFeedback
		accept bool
	}{
		{
			file:   "search-impression.json",
			accept: true,
			event: ports.SearchFeedback{
				Kind:     "impression",
				Receipt:  wireReceipt,
				OfferIDs: []string{"seller-a~p42"},
			},
		},
		{
			file: "search-click.json",
			event: ports.SearchFeedback{
				Kind:     "click",
				Receipt:  wireReceipt,
				OfferID:  "seller-a~p42",
				BuyerKey: wireBuyerKey,
			},
		},
	} {
		t.Run(item.file, func(t *testing.T) {
			expected := compactFeedbackFixture(t, item.file)
			client := feedbackSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
				assertFeedbackRequest(t, r, "/feedback", strings.Repeat("s", 32))
				assertFeedbackBody(t, r, expected)
				_ = json.NewEncoder(w).Encode(feedbackResult{Data: item.accept})
			})
			accepted, err := client.Feedback(context.Background(), item.event)
			if err != nil || accepted != item.accept {
				t.Fatal(accepted, err)
			}
		})
	}
}

func TestSearchPurchaseWire(t *testing.T) {
	expected := compactFeedbackFixture(t, "search-purchase.json")
	var event ports.SearchPurchase
	if err := json.Unmarshal(expected, &event); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	client := feedbackSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertFeedbackRequest(t, r, "/search-purchases", strings.Repeat("p", 32))
		assertFeedbackBody(t, r, expected)
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			_, _ = io.WriteString(w, `{"data":{"attributed":1}}`)
		case 3:
			_, _ = io.WriteString(w, `{"data":{"attributed":0}}`)
		case 4:
			w.WriteHeader(http.StatusUnauthorized)
		default:
			_, _ = io.WriteString(w, `{"data":{}}`)
		}
	})
	ctx := context.Background()
	if err := client.Purchase(ctx, event); !errors.Is(err, ErrUnavailable) {
		t.Fatal("HTTP 503 должен повторяться", err)
	}
	for range 2 {
		if err := client.Purchase(ctx, event); err != nil {
			t.Fatal("результат атрибуции терминальный, в том числе нулевой", err)
		}
	}
	for range 2 {
		if err := client.Purchase(ctx, event); errs.RetryKindOf(err) != errs.RetryPermanent {
			t.Fatal("неисправимое событие либо ответ нельзя повторять", err)
		}
	}
	if calls.Load() != 5 {
		t.Fatal("адаптер не должен повторять POST самостоятельно", calls.Load())
	}
}

func feedbackSearchClient(t *testing.T, handler http.HandlerFunc) *Index {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(Config{
		URL:           server.URL,
		Token:         strings.Repeat("s", 32),
		PurchaseToken: strings.Repeat("p", 32),
		Timeout:       time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func compactFeedbackFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatal(err)
	}
	return compact.Bytes()
}

func assertFeedbackRequest(t *testing.T, request *http.Request, path, token string) {
	t.Helper()
	if request.Method != http.MethodPost || request.URL.Path != apiPath+path ||
		request.Header.Get("X-Admin-Id") != token || request.Header.Get("Content-Type") != "application/json" {
		t.Errorf("неожиданный запрос %s %s", request.Method, request.URL.Path)
	}
}

func assertFeedbackBody(t *testing.T, request *http.Request, expected []byte) {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil || !bytes.Equal(body, expected) {
		t.Errorf("получено %s, ожидалось %s: %v", body, expected, err)
	}
}
