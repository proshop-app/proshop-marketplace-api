package platformapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/platformapi"
)

func TestDispatchReviewRequiresGenerationAndSendsItOnlyWithRecalculation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		data  string
		valid bool
	}{
		{"generation", `,"data":{"dispatchReviewRevision":3}`, true},
		{"missing", "", false},
		{"zero", `,"data":{"dispatchReviewRevision":0}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == prefix+"/orders" {
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"code":"DISPATCH_ORIGIN_CHANGED"` + tc.data + `}`))
					return
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				var review ports.DispatchReview
				if err := json.Unmarshal(body["dispatchReview"], &review); err != nil ||
					review.ExternalCheckoutID != "checkout1:t1" || review.GroupID != "t1" || review.Revision != 3 {
					t.Error(review, err)
				}
				_, _ = w.Write(fixture(t, "delivery_quote_available.json"))
			}))
			defer srv.Close()
			client := platformapi.New(platformapi.Config{BaseURL: srv.URL, Timeout: time.Second, Conns: 4})
			_, err := client.CreateOrder(context.Background(), "t1", orderRequest())
			var refusal *ports.OrderRefusal
			if !tc.valid {
				if !errors.Is(err, platformapi.ErrInvalidResponse) {
					t.Fatal(err)
				}
				return
			}
			if !errors.As(err, &refusal) || refusal.DispatchReviewRevision != 3 ||
				!errors.Is(err, ports.ErrDispatchOriginChanged) || refusal.Code != checkout.DispatchOriginChanged {
				t.Fatal(err)
			}
			_, err = client.QuoteDelivery(context.Background(), "t1", ports.QuoteRequest{
				DispatchReview: &ports.DispatchReview{
					ExternalCheckoutID: "checkout1:t1",
					GroupID:            "t1",
					Revision:           refusal.DispatchReviewRevision,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
