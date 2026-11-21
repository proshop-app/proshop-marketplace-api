package catalog_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	app "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestRecommendationRulesAndSingleCandidateQuery(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	seedSeller(store, "t1", participant.TenantActive, true)
	platform := fake.NewPlatform()
	features := &fake.CatalogFeatures{}
	queries := app.NewQueries(store, fake.NewIndex(), platform, features, features, fake.NewClock(time.Time{}))
	result, err := queries.Recommend(ctx, app.RecommendationRequest{
		Context: app.RecommendationContextHome,
		Region:  "RU-MOW",
		Limit:   5,
	})
	if err != nil || result.Reason != "popular" || features.Calls != 1 || features.Filter.Region != "RU-MOW" {
		t.Fatal(result, err, features)
	}
	features.BuyerInterests = ports.BuyerInterestSet{
		Categories: []string{"c1"},
		Purchased:  []ids.OfferID{{Tenant: "t1", Product: "p1"}},
	}
	result, err = queries.Recommend(ctx, app.RecommendationRequest{
		Context: app.RecommendationContextHome,
		BuyerID: "buyer",
		Limit:   5,
	})
	if err != nil || result.Reason != "popular" || features.Calls != 3 ||
		len(features.Filter.Categories) != 0 || len(features.Filter.Excluded) != 1 {
		t.Fatal(result, err, features)
	}
	seedProduct(store, "t1", "p1")
	result, err = queries.Recommend(ctx, app.RecommendationRequest{
		Context: app.RecommendationContextOffer,
		OfferID: ids.OfferID{Tenant: "t1", Product: "p1"},
		Limit:   5,
	})
	if err != nil || result.Reason != "similar" || features.Calls != 4 {
		t.Fatal(result, err)
	}
	f := features.Filter
	if f.OtherTenant != "t1" || len(f.Excluded) != 1 || f.Excluded[0].String() != "t1~p1" ||
		*f.PriceMin != 6000 || *f.PriceMax != 14000 || f.Categories[0] != "c1" {
		t.Fatal(f)
	}
	_, err = queries.Recommend(ctx, app.RecommendationRequest{Context: "bad"})
	if !errors.Is(err, app.ErrInvalidQuery) {
		t.Fatal(err)
	}
}

func TestReviewsVisibilityAndPagination(t *testing.T) {
	ctx := context.Background()
	index := fake.NewIndex()
	platform := fake.NewPlatform()
	platform.Reviews = []ports.Review{{Author: "Имя", Rating: 5, CreatedAt: t0}}
	q := app.NewQueries(fake.NewStore(), index, platform, nil, nil, fake.NewClock(time.Time{}))
	id := ids.OfferID{Tenant: "t1", Product: "p1"}
	if _, err := q.Reviews(ctx, id, 20, 0); !errors.Is(err, catalog.ErrOfferNotFound) {
		t.Fatal(err)
	}
	if err := index.Put(ctx, []ports.OfferDocument{{ID: id.String(), Visible: true, Version: 1}}); err != nil {
		t.Fatal(err)
	}
	page, err := q.Reviews(ctx, id, 0, 0)
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	if _, err := q.Reviews(ctx, id, 101, 0); !errors.Is(err, app.ErrInvalidQuery) {
		t.Fatal(err)
	}
	if _, err := q.Reviews(ctx, id, 20, 9990); !errors.Is(err, app.ErrInvalidQuery) {
		t.Fatal(err)
	}
	platform.ReviewsErr = errors.New("недоступен")
	if _, err := q.Reviews(ctx, id, 20, 0); err == nil {
		t.Fatal("ошибка потеряна")
	}
}

func TestRatingSortPlacesUnratedLast(t *testing.T) {
	ctx := context.Background()
	store, index := fake.NewStore(), fake.NewIndex()
	seedSeller(store, "t1", participant.TenantActive, true)
	for i, rating := range []*catalog.Rating{nil, {Value: 4.7, Count: 2}, {Value: 4.7, Count: 10}} {
		id := []string{"t1~p1", "t1~p2", "t1~p3"}[i]
		doc := ports.OfferDocument{
			ID:      id,
			Listed:  true,
			Visible: true,
			Version: 1,
			Rating:  rating,
		}
		if err := index.Put(ctx, []ports.OfferDocument{doc}); err != nil {
			t.Fatal(err)
		}
	}
	query := app.NewQueries(store, index, fake.NewPlatform(), nil, nil, fake.NewClock(time.Time{}))
	list, err := query.Search(ctx, ports.OfferQuery{Sort: ports.SortRating})
	if err != nil || len(list.Items) != 3 {
		t.Fatal(list, err)
	}
	if list.Items[0].Offer.ID != "t1~p3" || list.Items[1].Offer.ID != "t1~p2" || list.Items[2].Offer.Rating != nil {
		t.Fatal(list)
	}
}

func recommendationStates(t *testing.T, store *fake.Store) []ports.OfferState {
	t.Helper()
	ctx := context.Background()
	var states []ports.OfferState
	for _, tenant := range []ids.TenantID{"t1", "t2", "t3", "t4"} {
		seedSeller(store, tenant, participant.TenantActive, true)
		seedProduct(store, tenant, "p1")
		if err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			state, err := tx.Offers().Offer(ctx, tenant, "p1")
			if err != nil {
				return err
			}
			product := state.Product.State()
			if tenant == "t3" {
				product.Card.Visible = false
			}
			if tenant == "t4" {
				product.HasPrice = false
			}
			state.Product = catalog.RestoreProduct(product)
			states = append(states, state)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return states
}

func TestRecommendationAssemblyAndBuyerPrices(t *testing.T) {
	store := fake.NewStore()
	states := recommendationStates(t, store)
	seedProduct(store, "t1", "p0")
	platform := fake.NewPlatform()
	features := &fake.CatalogFeatures{States: states}
	q := app.NewQueries(store, fake.NewIndex(), platform, features, features, fake.NewClock(time.Time{}))
	request := app.RecommendationRequest{
		Context: app.RecommendationContextOffer,
		OfferID: ids.OfferID{Tenant: "t1", Product: "p0"},
		Limit:   10,
	}
	result, err := q.Recommend(context.Background(), request)
	if err != nil || result.Reason != "similar" || len(result.Items) != 2 {
		t.Fatal(result, err)
	}
	if result.Items[0].Offer.TenantID != "t2" || result.Items[1].Offer.TenantID != "t1" ||
		result.Items[0].Offer.Price != 10000 {
		t.Fatal(result)
	}
	if _, err := q.Recommend(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if features.Calls != 1 {
		t.Fatal("гостевой кэш не использован", features.Calls)
	}
	request.BuyerID = "buyer"
	result, err = q.Recommend(context.Background(), request)
	if err != nil || len(result.Items) != 2 || result.Items[0].Offer.Price != 10000 ||
		result.Items[0].Offer.BasePrice != 10000 {
		t.Fatal(result, err)
	}
	if features.Calls != 1 {
		t.Fatal("общий отбор карточки не использовал гостевой кэш")
	}
}

func TestPersonalFallbackReturnsPopular(t *testing.T) {
	store := fake.NewStore()
	states := recommendationStates(t, store)
	features := &fake.CatalogFeatures{
		States:         states,
		CategoryStates: map[string][]ports.OfferState{},
		BuyerInterests: ports.BuyerInterestSet{
			Categories: []string{"empty"},
			Purchased:  []ids.OfferID{{Tenant: "t5", Product: "p1"}},
		},
	}
	q := app.NewQueries(store, fake.NewIndex(), fake.NewPlatform(), features, features, fake.NewClock(time.Time{}))
	result, err := q.Recommend(context.Background(), app.RecommendationRequest{
		Context: app.RecommendationContextHome,
		BuyerID: "buyer",
	})
	if err != nil || result.Reason != "popular" || len(result.Items) != 2 || features.Calls != 2 {
		t.Fatal(result, err, features.Calls)
	}
	if len(features.Filter.Categories) != 0 || len(features.Filter.Excluded) != 1 {
		t.Fatal(features.Filter)
	}
	features.CategoryStates["empty"] = states
	result, err = q.Recommend(context.Background(), app.RecommendationRequest{
		Context: app.RecommendationContextHome,
		BuyerID: "buyer",
	})
	if err != nil || result.Reason != "personal" || len(result.Items) != 2 {
		t.Fatal(result, err)
	}
}

func TestGuestRecommendationExpiryAndSameTenant(t *testing.T) {
	store := fake.NewStore()
	states := recommendationStates(t, store)
	seedProduct(store, "t1", "p0")
	features := &fake.CatalogFeatures{States: states[:1]}
	clock := fake.NewClock(t0)
	q := app.NewQueries(store, fake.NewIndex(), fake.NewPlatform(), features, features, clock)
	request := app.RecommendationRequest{
		Context: app.RecommendationContextOffer,
		OfferID: ids.OfferID{Tenant: "t1", Product: "p0"},
	}
	for _, advance := range []time.Duration{0, time.Minute - time.Nanosecond, time.Nanosecond} {
		clock.Advance(advance)
		result, err := q.Recommend(context.Background(), request)
		if err != nil || len(result.Items) != 1 || result.Items[0].Offer.TenantID != "t1" {
			t.Fatal(result, err)
		}
		want := 1
		if advance == time.Nanosecond {
			want = 2
		}
		if features.Calls != want {
			t.Fatal("TTL гостевого кэша", features.Calls, want)
		}
	}
	request.Region = "unknown"
	if _, err := q.Recommend(context.Background(), request); !errors.Is(err, app.ErrInvalidQuery) {
		t.Fatal("неизвестный регион принят", err)
	}
	if features.Calls != 2 {
		t.Fatal("регион проверен после запроса", features.Calls)
	}
}

func TestGuestRecommendationsShareMissAndDoNotCacheErrors(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var first sync.Once
	features := &fake.CatalogFeatures{
		RecommendHook: func(ctx context.Context) error {
			first.Do(func() {
				close(started)
				<-release
			})
			if _, ok := ctx.Deadline(); !ok {
				t.Error("у общей загрузки нет таймаута")
			}
			return ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := app.NewQueries(fake.NewStore(), fake.NewIndex(), fake.NewPlatform(), features, features, fake.NewClock(t0))
	request := app.RecommendationRequest{Context: app.RecommendationContextHome}
	var wg sync.WaitGroup
	ready := make(chan struct{}, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		callCtx := context.Background()
		if i == 0 {
			callCtx = ctx
		}
		go func() {
			defer wg.Done()
			ready <- struct{}{}
			if _, err := q.Recommend(callCtx, request); err != nil && !errors.Is(err, callCtx.Err()) {
				t.Error(err)
			}
		}()
		if i == 0 {
			<-started
		}
	}
	for i := 0; i < 20; i++ {
		<-ready
	}
	// Первый ответ удерживается, пока остальные вызовы присоединяются к общей загрузке.
	time.Sleep(30 * time.Millisecond)
	cancel()
	close(release)
	wg.Wait()
	if features.Calls != 1 {
		t.Fatal("конкурентный промах не объединён", features.Calls)
	}
	request.Region = "RU-MOW"
	features.Err = errors.New("временный отказ")
	if _, err := q.Recommend(context.Background(), request); err == nil {
		t.Fatal("ошибка потеряна")
	}
	features.Err = nil
	if _, err := q.Recommend(context.Background(), request); err != nil {
		t.Fatal("ошибка закэширована", err)
	}
	if features.Calls != 3 {
		t.Fatal(features.Calls)
	}
}

func TestViewedRecommendationsForGuestAndBuyer(t *testing.T) {
	for _, buyer := range []ids.UUID{"", "buyer"} {
		store := fake.NewStore()
		states := recommendationStates(t, store)
		features := &fake.CatalogFeatures{
			States:         states[1:2],
			ViewCategories: []string{"c1"},
			BuyerInterests: ports.BuyerInterestSet{Categories: []string{"c2"}},
		}
		q := app.NewQueries(store, fake.NewIndex(), fake.NewPlatform(), features, features, fake.NewClock(t0))
		viewed := ids.OfferID{Tenant: "t1", Product: "p1"}
		result, err := q.Recommend(context.Background(), app.RecommendationRequest{
			Context: app.RecommendationContextHome,
			BuyerID: buyer,
			Viewed:  []ids.OfferID{viewed, viewed},
		})
		if err != nil || result.Reason != "personal" || len(result.Items) != 1 {
			t.Fatal(result, err)
		}
		if features.ViewedCalls != 1 || len(features.Viewed) != 1 ||
			len(features.Filter.Excluded) != 1 || features.Filter.Excluded[0] != viewed {
			t.Fatal(features)
		}
		wantCategories := 1
		if buyer != "" {
			wantCategories = 2
		}
		if len(features.Filter.Categories) != wantCategories || result.Items[0].Offer.ID == viewed.String() {
			t.Fatal(features.Filter, result)
		}
	}
}

func TestGuestRecommendationsCancelSQLWhenLastWaiterLeaves(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	features := &fake.CatalogFeatures{
		RecommendHook: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		},
	}
	q := app.NewQueries(fake.NewStore(), fake.NewIndex(), fake.NewPlatform(), features, features, fake.NewClock(t0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := q.Recommend(ctx, app.RecommendationRequest{Context: app.RecommendationContextHome})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("SQL общей загрузки пережил последнего клиента")
	}
}
