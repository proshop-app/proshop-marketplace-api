package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apppromotion "github.com/proshop-app/proshop-marketplace/internal/application/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

type cacheTestIndex struct {
	*fake.Index
	err error
}

func (i *cacheTestIndex) Search(ctx context.Context, query ports.OfferQuery) (ports.OfferPage, error) {
	if i.err != nil {
		return ports.OfferPage{}, i.err
	}
	return i.Index.Search(ctx, query)
}

func (i *cacheTestIndex) Get(ctx context.Context, id string) (ports.OfferDocument, bool, error) {
	if i.err != nil {
		return ports.OfferDocument{}, false, i.err
	}
	return i.Index.Get(ctx, id)
}

func (i *cacheTestIndex) Group(ctx context.Context, tenant, group string) (ports.OfferGroup, error) {
	if i.err != nil {
		return ports.OfferGroup{}, i.err
	}
	return i.Index.Group(ctx, tenant, group)
}

func TestCatalogPublicCacheOnlyOnSuccess(t *testing.T) {
	store := fake.NewStore()
	index := &cacheTestIndex{Index: fake.NewIndex()}
	if err := index.Put(context.Background(), []ports.OfferDocument{
		{
			ID:        "t1~p1",
			TenantID:  "t1",
			ProductID: "p1",
			Visible:   true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	clock := fake.NewClock(time.Time{})
	queries := appcatalog.NewQueries(store, index, nil, nil, nil, clock)
	catalog := catalogHandlers{queries: queries}
	promotion := promotionHandlers{service: apppromotion.NewService(store, clock, nil, index, apppromotion.Config{
		Catalog:  queries,
		Location: time.UTC,
		Random:   fake.Random{},
	})}
	for _, route := range []struct {
		name    string
		path    string
		invalid string
		handler http.HandlerFunc
	}{
		{"list", "/?q=item", "/?limit=invalid", catalog.list},
		{"card", "/", "/", catalog.card},
		{"variants", "/", "/", catalog.variants},
		{"sponsored", "/?q=item", "/?limit=invalid", promotion.sponsored},
		{"listIDs", "/?ids=t1~p1", "/?ids=invalid", catalog.list},
	} {
		t.Run(route.name, func(t *testing.T) {
			for _, status := range []int{200, 400, 404, 503} {
				t.Run(strconv.Itoa(status), func(t *testing.T) {
					index.err = nil
					path := route.path
					if status == 400 {
						path = route.invalid
					}
					if status == 404 {
						index.err = errs.New(errs.NotFound, "catalog.offer_not_found", "не найдено")
					}
					if status == 503 {
						index.err = errs.New(errs.Unavailable, "catalog.unavailable", "каталог недоступен")
					}
					request := httptest.NewRequest(http.MethodGet, path, nil)
					request.SetPathValue("offerId", "t1~p1")
					request.SetPathValue("tenantId", "t1")
					request.SetPathValue("groupId", "group")
					if status == 400 {
						request.SetPathValue("offerId", "invalid")
						request.SetPathValue("tenantId", "")
					}
					response := httptest.NewRecorder()
					route.handler(response, request)
					want := status
					// Пакетное чтение переводит любой отказ индекса в 503.
					if route.name == "listIDs" && status == 404 {
						want = 503
					}
					assertCatalogCache(t, response, want)
				})
			}
		})
	}
}

func assertCatalogCache(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	cache := response.Result().Header.Get("Cache-Control")
	if response.Code != status || strings.Contains(cache, "public") != (status == http.StatusOK) {
		t.Fatal(response.Code, cache, response.Body.String())
	}
	if status == http.StatusOK && cache != "public, max-age=30" {
		t.Fatal(cache)
	}
}

func TestCatalogCanceledSellerPopulationIsNotPublic(t *testing.T) {
	index := fake.NewIndex()
	handler := catalogHandlers{queries: appcatalog.NewQueries(fake.NewStore(), index, nil, nil, nil, nil)}
	for _, route := range []struct {
		name    string
		path    string
		handler http.HandlerFunc
	}{
		{"list", "/", handler.list},
		{"listIDs", "/?ids=t1~p1", handler.list},
		{"card", "/", handler.card},
	} {
		t.Run(route.name, func(t *testing.T) {
			if err := index.Put(context.Background(), []ports.OfferDocument{{ID: "t1~p1", Visible: true}}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			request := httptest.NewRequest(http.MethodGet, route.path, nil).WithContext(ctx)
			request.SetPathValue("offerId", "t1~p1")
			response := httptest.NewRecorder()
			route.handler(response, request)
			if response.Body.Len() != 0 || strings.Contains(response.Result().Header.Get("Cache-Control"), "public") {
				t.Fatal(response.Header(), response.Body.String())
			}
		})
	}
}
