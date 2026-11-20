package searchapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestSearchClassifiesBadRequestWithoutRetry(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			transport := featureTransport(func(r *http.Request) (*http.Response, error) {
				defer func() {
					_ = r.Body.Close()
				}()
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/internal/marketplace/search" {
					t.Errorf("неверный запрос: %s %s", r.Method, r.URL.Path)
				}
				w := httptest.NewRecorder()
				http.Error(w, "служебная ошибка", status)
				return w.Result(), nil
			})
			index := &Index{
				base:   "http://search.test" + apiPath,
				client: &http.Client{Transport: transport},
			}
			_, err := index.Search(context.Background(), ports.OfferQuery{Text: "Товар"})
			if calls != 1 {
				t.Fatalf("вызовов поиска: %d", calls)
			}
			if status == http.StatusBadRequest && !errors.Is(err, ErrInvalid) {
				t.Fatalf("ожидалась публичная ошибка запроса: %v", err)
			}
			if status == http.StatusServiceUnavailable && !errors.Is(err, ErrUnavailable) {
				t.Fatalf("сбой поиска потерял класс: %v", err)
			}
		})
	}
}
