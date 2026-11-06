package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestCatalogFeaturesHTTP(t *testing.T) {
	store, index, platform := fake.NewStore(), fake.NewIndex(), fake.NewPlatform()
	features := &fake.CatalogFeatures{}
	store.SeedTaxonomy(taxonomy.Definition{})
	q := appcatalog.NewQueries(store, index, platform, features, features, fake.NewClock(time.Time{}))
	h := catalogHandlers{queries: q}
	platform.Reviews = []ports.Review{{Author: "Имя", Rating: 5, CreatedAt: time.Now()}}
	if err := index.Put(context.Background(), []ports.OfferDocument{{
		ID:      "t1~p1",
		Visible: true,
		Listed:  true,
		Version: 1,
		Rating:  &catalog.Rating{Value: 4.5, Count: 2},
		PriceProfiles: []catalog.PriceProfile{{
			Key:            catalog.DefaultPriceProfile,
			Price:          money.FromKopecks(100),
			EffectivePrice: money.FromKopecks(100),
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path    string
		handler http.HandlerFunc
		status  int
	}{
		{"/reviews?limit=1&offset=0", h.reviews, 200},
		{"/reviews?limit=101", h.reviews, 400},
		{"/reviews?offset=-1", h.reviews, 400},
		{"/reviews?limit=bad", h.reviews, 400},
		{"/recommendations?context=home&deliveryRegion=RU-MOW", h.recommendations, 200},
		{"/recommendations?context=bad", h.recommendations, 400},
		{"/recommendations?context=offer", h.recommendations, 400},
		{"/offers?sort=rating", h.list, 200},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.SetPathValue("offerId", "t1~p1")
			w := httptest.NewRecorder()
			tc.handler(w, r)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/reviews", nil)
	r.SetPathValue("offerId", "t1~p1")
	h.reviews(w, r)
	if w.Header().Get("Cache-Control") != "public, max-age=30" {
		t.Fatal(w.Header())
	}
	var page ports.ReviewPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Total != 1 {
		t.Fatal(page, err)
	}
	index.Facets = []ports.Facet{{
		Code:    "price",
		Type:    "number",
		Count:   1,
		Buckets: []ports.PriceBucket{{From: 0, To: 1000, Count: 1}},
	}}
	list := httptest.NewRecorder()
	h.list(list, httptest.NewRequest(http.MethodGet, "/offers?sort=rating", nil))
	var offers offerPageJSON
	if err := json.Unmarshal(list.Body.Bytes(), &offers); err != nil || len(offers.Items) != 1 {
		t.Fatal(list.Body.String(), err)
	}
	if !strings.Contains(list.Body.String(), `"buckets":[{"from":0,"to":1000,"count":1}]`) {
		t.Fatal(list.Body.String())
	}
	rating := offers.Items[0].Rating
	if rating == nil || rating.Value != 4.5 || rating.Count != 2 {
		t.Fatal(rating)
	}
	item := toOfferJSON(appcatalog.OfferCard{}, false)
	if item.Rating != nil {
		t.Fatal(item)
	}
}

func TestRecommendationsPrivateResponse(t *testing.T) {
	store, index, platform := fake.NewStore(), fake.NewIndex(), fake.NewPlatform()
	features := &fake.CatalogFeatures{}
	queries := appcatalog.NewQueries(store, index, platform, features, features, fake.NewClock(time.Time{}))
	h := catalogHandlers{queries: queries}
	for _, buyer := range []ids.UUID{"", "buyer"} {
		request := httptest.NewRequest(http.MethodGet, "/recommendations?context=home", nil)
		request = request.WithContext(context.WithValue(request.Context(), keyBuyer, buyer))
		response := httptest.NewRecorder()
		h.recommendations(response, request)
		if response.Code != 200 || response.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal(response.Code, response.Header(), response.Body.String())
		}
		var body recommendationsJSON
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil ||
			body.Items == nil || body.Reason != "popular" {
			t.Fatal(body, err)
		}
	}
}

func TestReviewsHiddenAndUnavailable(t *testing.T) {
	store, index, platform := fake.NewStore(), fake.NewIndex(), fake.NewPlatform()
	h := catalogHandlers{queries: appcatalog.NewQueries(store, index, platform, nil, nil, fake.NewClock(time.Time{}))}
	request := httptest.NewRequest(http.MethodGet, "/reviews", nil)
	request.SetPathValue("offerId", "t1~p1")
	hidden := ports.OfferDocument{ID: "t1~p1", Visible: false, Version: 1}
	if err := index.Put(context.Background(), []ports.OfferDocument{hidden}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	h.reviews(response, request)
	if response.Code != 404 {
		t.Fatal(response.Code, response.Body.String())
	}
	visible := ports.OfferDocument{ID: "t1~p1", Visible: true, Version: 2}
	if err := index.Put(context.Background(), []ports.OfferDocument{visible}); err != nil {
		t.Fatal(err)
	}
	platform.ReviewsErr = errs.New(errs.Unavailable, "platform.reviews_unavailable", "отзывы недоступны")
	response = httptest.NewRecorder()
	h.reviews(response, request)
	if response.Code != 503 {
		t.Fatal(response.Code, response.Body.String())
	}
}
