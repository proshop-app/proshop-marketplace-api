package http

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apppromotion "github.com/proshop-app/proshop-marketplace/internal/application/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

type queryTextStore struct {
	*fake.Store
	calls int
}

type queryTextIndex struct {
	fake.PromotionIndex
	err   error
	calls int
}

func (s *queryTextStore) Do(ctx context.Context, run func(context.Context, ports.Tx) error) error {
	s.calls++
	return s.Store.Do(ctx, run)
}

func (i *queryTextIndex) Search(ctx context.Context, query ports.OfferQuery) (ports.OfferPage, error) {
	i.calls++
	if i.err != nil {
		return ports.OfferPage{}, i.err
	}
	return i.PromotionIndex.Search(ctx, query)
}

func TestOfferSearchQueryTextHTTP(t *testing.T) {
	store := &queryTextStore{Store: fake.NewStore()}
	index := &queryTextIndex{}
	clock := fake.NewClock(time.Time{})
	tokens := fake.NewIdentityTokens(clock)
	access, err := tokens.Open(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	queries := appcatalog.NewQueries(store, index, fake.NewPlatform(), nil, nil, clock)
	var logs bytes.Buffer
	handler := NewHandler(Deps{
		RequestTimeout:         time.Minute,
		ReferenceImportTimeout: time.Minute,
		Auth:                   appbuyer.NewAuth(store, nil, tokens, nil, nil, clock, appbuyer.AuthConfig{}),
		Catalog:                queries,
		Promotion: apppromotion.NewService(store, clock, nil, index, apppromotion.Config{
			Catalog:  queries,
			Location: time.UTC,
			Random:   fake.Random{},
		}),
		Registerer: prometheus.NewRegistry(),
		Logger:     slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	for _, path := range []string{
		"/api/v1/catalog/offers/t1~p1",
		"/api/v1/catalog/variants/t1/group",
	} {
		store.calls = 0
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set(moneyUnitHeader, "RUB")
		r.Header.Set("Authorization", "Bearer "+access.Token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		if store.calls != 0 || response.Header().Get("Cache-Control") != "public, max-age=30" {
			t.Fatal(path, store.calls, response.Header())
		}
	}

	text32 := "\ufeff" + strings.Repeat("я\u0085", 16) + strings.Repeat("я\ufeff", 15) + "я\u200bя\u180eя \t"
	for _, path := range []string{"/api/v1/catalog/offers", "/api/v1/catalog/sponsored"} {
		t.Run(path, func(t *testing.T) {
			request := func(text, suffix string, withToken bool) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodGet, path+"?q="+url.QueryEscape(text)+suffix, nil)
				r.Header.Set(moneyUnitHeader, "RUB")
				if withToken {
					r.Header.Set("Authorization", "Bearer "+access.Token)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, r)
				return response
			}

			store.calls, index.calls = 0, 0
			response := request(text32, "", false)
			if response.Code != http.StatusOK || index.calls != 1 {
				t.Fatal(response.Code, response.Body.String(), index.calls)
			}

			guestCalls := store.calls
			store.calls = 0
			response = request(text32, "", true)
			if response.Code != http.StatusOK || store.calls != guestCalls ||
				response.Header().Get("Cache-Control") != "public, max-age=30" {
				t.Fatal("публичная выдача зависит от входа", response.Code, store.calls, guestCalls, response.Header())
			}

			store.calls, index.calls = 0, 0
			response = request(text32+"я", "", true)
			assertOfferSearchRejection(t, response, "catalog.invalid_query", "строка поиска содержит больше 32 слов")
			response = request(text32+"я", "&offset=1", true)
			assertOfferSearchRejection(t, response, "catalog.invalid_query", "строка поиска содержит больше 32 слов")
			response = request(strings.Repeat("я", 201), "", true)
			assertOfferSearchRejection(t, response, "catalog.invalid_query", "строка поиска длиннее 200 символов")
			if store.calls != 0 || index.calls != 0 {
				t.Fatalf("невалидный текст дошёл до базы (%d) или поиска (%d)", store.calls, index.calls)
			}

			logs.Reset()
			index.err = errs.New(errs.Invalid, "search.invalid_request", "некорректный запрос поиска")
			response = request(text32, "", false)
			index.err = nil
			assertOfferSearchRejection(t, response, "search.invalid_request", "некорректный запрос поиска")
			if index.calls != 1 || logs.Len() != 0 {
				t.Fatalf("вызовов поиска: %d, журнал ошибок: %s", index.calls, logs.String())
			}
		})
	}
}

func assertOfferSearchRejection(t *testing.T, response *httptest.ResponseRecorder, code, message string) {
	t.Helper()
	var body errorBody
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusBadRequest || body.Error.Code != code || body.Error.Message != message {
		t.Fatal(response.Code, response.Body.String())
	}
}
