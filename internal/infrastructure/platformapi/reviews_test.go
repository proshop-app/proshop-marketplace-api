package platformapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

type reviewTransport func(*http.Request) (*http.Response, error)

func (f reviewTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReviewsContract(t *testing.T) {
	for _, name := range []string{"reviews.json", "reviews_invalid.json"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/" + name)
			if err != nil {
				t.Fatal(err)
			}
			client := New(Config{BaseURL: "http://platform.test", Token: "service-token", Timeout: time.Second})
			client.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
				path := "/api/v1/internal/marketplace/tenants/t1/products/p1/reviews"
				if r.Method != http.MethodGet || r.URL.Path != path ||
					r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("offset") != "3" ||
					r.Header.Get(HeaderToken) != "service-token" {
					t.Error(r.URL)
				}
				w := httptest.NewRecorder()
				_, _ = w.Write(raw)
				return w.Result(), nil
			})
			page, err := client.ProductReviews(context.Background(), "t1", "p1", 2, 3)
			if name == "reviews_invalid.json" {
				if !errors.Is(err, ErrInvalidResponse) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || page.Total != 12 || len(page.Items) != 1 || page.Items[0].Author != "Имя" {
				t.Fatal(page, err)
			}
		})
	}
}
