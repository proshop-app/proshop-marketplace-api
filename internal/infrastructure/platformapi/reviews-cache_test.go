package platformapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
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
			client := New(Config{BaseURL: "http://platform.test"})
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
		client := New(Config{BaseURL: "http://platform.test"})
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

func TestReviewsCacheSingleflightAndExpiry(t *testing.T) {
	raw, err := os.ReadFile("testdata/reviews.json")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := New(Config{BaseURL: "http://platform.test"})
	client.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			if err := r.Context().Err(); err != nil {
				return nil, err
			}
		}
		w := httptest.NewRecorder()
		_, _ = w.Write(raw)
		return w.Result(), nil
	})
	var wg sync.WaitGroup
	ready := make(chan struct{}, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		callCtx := context.Background()
		if i == 0 {
			callCtx = ctx
		}
		go func() {
			ready <- struct{}{}
			defer wg.Done()
			page, err := client.ProductReviews(callCtx, "t1", "p1", 20, 0)
			if err != nil || len(page.Items) != 1 {
				t.Error(page, err)
				return
			}
			page.Items[0].Author = "Изменён"
		}()
		if i == 0 {
			<-started
		}
	}
	for i := 0; i < 20; i++ {
		<-ready
	}
	// Транспорт удерживает первый ответ, пока конкурентные вызовы присоединяются.
	time.Sleep(30 * time.Millisecond)
	cancel()
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	page, err := client.ProductReviews(context.Background(), "t1", "p1", 20, 0)
	if err != nil || page.Items[0].Author != "Имя" {
		t.Fatal(page, err)
	}
	client.reviewsMu.Lock()
	for key, entry := range client.reviews {
		entry.expires = time.Now().Add(-time.Second)
		client.reviews[key] = entry
	}
	client.reviewsMu.Unlock()
	if _, err := client.ProductReviews(context.Background(), "t1", "p1", 20, 0); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	if _, err := client.ProductReviews(context.Background(), "t1", "p1", 20, 1); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatal(calls.Load())
	}
}

func TestReviewsCancellationAndErrorsAreNotCached(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	client := New(Config{BaseURL: "http://platform.test"})
	client.http.Transport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		if call == 1 {
			close(started)
			<-release
			if err := r.Context().Err(); err != nil {
				return nil, err
			}
			return nil, errors.New("временный отказ")
		}
		w := httptest.NewRecorder()
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
		return w.Result(), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() {
		_, err := client.ProductReviews(ctx, "t1", "p1", 20, 0)
		first <- err
	}()
	<-started
	cancel()
	close(release)
	if err := <-first; err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("отмена вызывающего прервала общую загрузку", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := client.ProductReviews(context.Background(), "t1", "p1", 20, 0); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("ошибка закэширована или успешный ответ не закэширован", calls.Load())
	}
}
