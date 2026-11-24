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

func TestReviewsEmptyAuthorAndNull(t *testing.T) {
	for _, name := range []string{"reviews_empty_author.json", "reviews_null.json"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/" + name)
			if err != nil {
				t.Fatal(err)
			}
			client := New(Config{BaseURL: "http://platform.test", Timeout: time.Second, Conns: 4})
			client.http.Transport = reviewTransport(func(*http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				_, _ = w.Write(raw)
				return w.Result(), nil
			})
			page, err := client.ProductReviews(context.Background(), "t1", "p1", 20, 0)
			if err != nil || page.Items == nil {
				t.Fatal(page, err)
			}
			if name == "reviews_empty_author.json" && (len(page.Items) != 1 || page.Items[0].Author != "") {
				t.Fatal(page)
			}
			if name == "reviews_null.json" && (len(page.Items) != 0 || page.Total != 0) {
				t.Fatal(page)
			}
		})
	}
}

func TestReviewsRefusals(t *testing.T) {
	raw, err := os.ReadFile("testdata/error.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{400, 401, 403, 404, 409, 429, 500, 503} {
		client := New(Config{BaseURL: "http://platform.test", Timeout: time.Second, Conns: 4})
		client.http.Transport = reviewTransport(func(*http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			w.WriteHeader(status)
			_, _ = w.Write(raw)
			return w.Result(), nil
		})
		_, err := client.ProductReviews(context.Background(), "t1", "p1", 20, 0)
		expected := ErrReviewsUnavailable
		if status == 404 {
			expected = ErrReviewsNotFound
		}
		if !errors.Is(err, expected) {
			t.Fatalf("status %d: %v", status, err)
		}
	}
}

func TestReviewsPreserveCallerDeadline(t *testing.T) {
	client := New(Config{BaseURL: "http://platform.test", Timeout: time.Second, Conns: 4})
	client.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := client.ProductReviews(ctx, "t1", "p1", 20, 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("общая загрузка потеряла бюджет витринного блока", err)
	}
}
