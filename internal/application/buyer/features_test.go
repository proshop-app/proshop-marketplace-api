package buyer_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	app "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const featureBuyer ids.UUID = "7a1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b"

func seedFeatureOffer(store *fake.Store, price int64, visible bool) {
	store.SeedTenant(participant.TenantState{TenantID: "t1", Status: participant.TenantActive})
	store.SeedParticipant(participant.State{
		TenantID: "t1",
		Enabled:  true,
		Stage:    participant.StageConnected,

		Delivery: participant.DeliverySettings{Regions: []string{"RU-AD"}, Carriers: []string{"cdek", "yandex"}},
	})
	store.SeedProduct(catalog.ProductState{
		TenantID:  "t1",
		ProductID: "p1",
		Available: 5,
		Card:      catalog.Card{Name: "Товар", Visible: visible, Publication: taxonomy.Publication{Published: true}},
		HasPrice:  true,
		Price:     money.FromKopecks(price),
		BasePrice: money.FromKopecks(price),
	})
	store.SeedVerdict(moderation.VerdictState{TenantID: "t1", ProductID: "p1", Status: moderation.Allowed})
}

func TestSavedPriceAndUnavailable(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	service := app.NewFeatures(store, nil, &fake.PickupLookup{})
	seedFeatureOffer(store, 100, true)
	offer := ids.OfferID{Tenant: "t1", Product: "p1"}
	for _, kind := range []ports.SavedKind{ports.Favorites, ports.PriceWatches} {
		if err := service.PutSaved(ctx, featureBuyer, kind, offer, nil); err != nil {
			t.Fatal(err)
		}
	}
	seedFeatureOffer(store, 80, true)
	// Повтор не сбрасывает исходную цену на текущую.
	if err := service.PutSaved(ctx, featureBuyer, ports.PriceWatches, offer, nil); err != nil {
		t.Fatal(err)
	}
	items, err := service.Saved(ctx, featureBuyer)
	if err != nil || len(items) != 1 || items[0].PriceAtWatch != 100 || !items[0].Dropped {
		t.Fatalf("снижение: %+v %v", items, err)
	}
	profile, err := service.Profile(ctx, featureBuyer)
	if err != nil || profile.Alerts.PriceDrops != 1 {
		t.Fatalf("счётчик: %+v %v", profile, err)
	}
	other, _, err := service.Favorites(ctx, "other", 20, 0)
	if err != nil || len(other) != 0 {
		t.Fatal("утечка чужого списка")
	}
	seedFeatureOffer(store, 80, false)
	items, _, err = service.Favorites(ctx, featureBuyer, 20, 0)
	if err != nil || len(items) != 1 || items[0].Listed || items[0].Offer.Name != "Товар" ||
		items[0].Offer.InStock || items[0].Offer.CheckoutAvailable || items[0].Offer.Available != 0 {
		t.Fatalf("снятое избранное: %+v %v", items, err)
	}
	watches, err := service.Saved(ctx, featureBuyer)
	if err != nil || watches[0].CurrentPrice != nil || watches[0].Dropped || watches[0].Offer.InStock ||
		watches[0].Offer.CheckoutAvailable || watches[0].Offer.Available != 0 {
		t.Fatal("снятый товар не подешевел")
	}
	for i := 0; i < 2; i++ {
		if err := service.DeleteSaved(ctx, featureBuyer, ports.Favorites, offer.String()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFeatureLimitAndRollback(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	service := app.NewFeatures(store, nil, &fake.PickupLookup{})
	seedFeatureOffer(store, 100, true)
	err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for i := 0; i < buyer.FavoritesLimit; i++ {
			key := ids.OfferID{Tenant: "t1", Product: ids.ProductID(fmt.Sprintf("saved-%d", i))}
			if err := tx.BuyerFeatures().PutSaved(ctx, featureBuyer, ports.Favorites, ports.SavedOffer{
				Offer: ports.OfferSnapshot{ID: key.String()},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	offer := ids.OfferID{Tenant: "t1", Product: "p1"}
	err = service.PutSaved(ctx, featureBuyer, ports.Favorites, offer, nil)
	if !errors.Is(err, buyer.ErrCollectionLimit) {
		t.Fatal(err)
	}
	store.FailCommit = errors.New("rollback")
	if _, err := service.Patch(ctx, featureBuyer, func(state buyer.ProfileState) (buyer.ProfileState, error) {
		state.Name = "Имя"
		return state, nil
	}); err == nil {
		t.Fatal("нет ошибки фиксации")
	}
	store.FailCommit = nil
	profile, err := service.Profile(ctx, featureBuyer)
	if err != nil || profile.Name != "" {
		t.Fatal("профиль не откатился")
	}
}

func TestRegionPriority(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	detector := &fake.Cities{Detection: ports.DetectedCity{
		City:       "Город",
		Country:    "RU",
		RegionCode: "RU-AD",
	}}
	service := app.NewFeatures(store, detector, &fake.PickupLookup{})
	const ip = "192.0.2.1"

	result, err := service.Region(ctx, featureBuyer, ip)
	if err != nil || result.Source != "ip" || result.Code == nil || *result.Code != "RU-AD" ||
		result.Name == nil || *result.Name != "Республика Адыгея" || len(detector.IPs) != 1 || detector.IPs[0] != ip {
		t.Fatalf("IP: %+v %v", result, err)
	}
	err = store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p := buyer.RestoreProfile(buyer.ProfileState{})
		_, err := p.SelectPickup(buyer.Pickup{Provider: "cdek", Code: "p1", RegionCode: "RU-KR"})
		if err != nil {
			return err
		}
		return tx.BuyerFeatures().SaveProfile(ctx, featureBuyer, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err = service.Region(ctx, featureBuyer, ip)
	if err != nil || result.Source != "pickup" || result.Code == nil || *result.Code != "RU-KR" ||
		len(detector.IPs) != 1 {
		t.Fatal(result, err)
	}
	err = store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.BuyerFeatures().Profile(ctx, featureBuyer)
		if err != nil {
			return err
		}
		state := p.State()
		state.Region = &buyer.Region{Code: "RU-AL"}
		if _, err := p.Change(state); err != nil {
			return err
		}
		return tx.BuyerFeatures().SaveProfile(ctx, featureBuyer, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err = service.Region(ctx, featureBuyer, ip)
	if err != nil || result.Source != "profile" || result.Code == nil || *result.Code != "RU-AL" ||
		len(detector.IPs) != 1 {
		t.Fatal(result, err)
	}
}

func TestSubscriptionsAreIdempotentAndCounted(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	service := app.NewFeatures(store, nil, &fake.PickupLookup{})
	err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.StoreCards().Save(ctx, ports.StoreRecord{
			PagePublic: true,
			Header:     ports.ShopHeader{TenantID: "t1", Slug: "shop"},
			Card:       ports.ShopCard{TenantID: "t1", Slug: "shop"},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := service.Subscribe(ctx, featureBuyer, "t1", ports.Subscribed); err != nil {
			t.Fatal(err)
		}
	}
	shops, err := service.Subscriptions(ctx, featureBuyer)
	if err != nil || len(shops) != 1 || shops[0].TenantID != "t1" {
		t.Fatal(shops, err)
	}
	count, subscribed, err := service.Subscribers(ctx, "t1", featureBuyer)
	if err != nil || count != 1 || subscribed == nil || !*subscribed {
		t.Fatal(count, subscribed, err)
	}
	count, subscribed, err = service.Subscribers(ctx, "t1", "")
	if err != nil || count != 1 || subscribed != nil {
		t.Fatal(count, subscribed, err)
	}
	for i := 0; i < 2; i++ {
		if err := service.Subscribe(ctx, featureBuyer, "t1", ports.Unsubscribed); err != nil {
			t.Fatal(err)
		}
	}
	count, subscribed, err = service.Subscribers(ctx, "t1", featureBuyer)
	if err != nil || count != 0 || subscribed == nil || *subscribed {
		t.Fatal(count, subscribed, err)
	}
}

func TestWatchTargetChangeAndFavoritesPagination(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	service := app.NewFeatures(store, nil, &fake.PickupLookup{})
	seedFeatureOffer(store, 100, true)
	offer := ids.OfferID{Tenant: "t1", Product: "p1"}
	if err := service.PutSaved(ctx, featureBuyer, ports.PriceWatches, offer, nil); err != nil {
		t.Fatal(err)
	}
	seedFeatureOffer(store, 90, true)
	for _, test := range []struct {
		target  int64
		dropped bool
	}{{80, false}, {95, true}, {120, true}} {
		target := money.FromKopecks(test.target)
		if err := service.PutSaved(ctx, featureBuyer, ports.PriceWatches, offer, &target); err != nil {
			t.Fatal(err)
		}
		items, err := service.Saved(ctx, featureBuyer)
		if err != nil || len(items) != 1 || items[0].PriceAtWatch != 100 || items[0].Dropped != test.dropped {
			t.Fatal(items, err)
		}
	}
	if err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for _, id := range []string{"t1~p1", "t1~p2", "t1~p3"} {
			if err := tx.BuyerFeatures().PutSaved(ctx, featureBuyer, ports.Favorites, ports.SavedOffer{
				Offer: ports.OfferSnapshot{ID: id},
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	items, total, err := service.Favorites(ctx, featureBuyer, 1, 1)
	if err != nil || total != 3 || len(items) != 1 || items[0].Offer.ID != "t1~p2" {
		t.Fatal(items, total, err)
	}
	items, total, err = service.Favorites(ctx, featureBuyer, 1, 20)
	if err != nil || total != 3 || len(items) != 0 {
		t.Fatal(items, total, err)
	}
}

func TestWatchAndSubscriptionLimits(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	service := app.NewFeatures(store, nil, &fake.PickupLookup{})
	if err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for i := 0; i < buyer.PriceWatchesLimit; i++ {
			if err := tx.BuyerFeatures().PutSaved(ctx, featureBuyer, ports.PriceWatches, ports.SavedOffer{
				Offer: ports.OfferSnapshot{ID: fmt.Sprintf("t1~saved-%d", i)},
			}); err != nil {
				return err
			}
		}
		for i := 0; i < buyer.SubscriptionsLimit; i++ {
			tenant := ids.TenantID(fmt.Sprintf("shop-%d", i))
			if err := tx.BuyerFeatures().Subscribe(ctx, featureBuyer, tenant, ports.Subscribed); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err := service.PutSaved(ctx, featureBuyer, ports.PriceWatches, ids.OfferID{Tenant: "t1", Product: "p1"}, nil)
	if !errors.Is(err, buyer.ErrCollectionLimit) {
		t.Fatal(err)
	}
	err = service.Subscribe(ctx, featureBuyer, "new-shop", ports.Subscribed)
	if !errors.Is(err, buyer.ErrCollectionLimit) {
		t.Fatal(err)
	}
	target := money.FromKopecks(90)
	existing := ids.OfferID{Tenant: "t1", Product: "saved-0"}
	err = service.PutSaved(ctx, featureBuyer, ports.PriceWatches, existing, &target)
	if err != nil {
		t.Fatal("цель существующего наблюдения при полном списке", err)
	}
}

func TestRegionWithoutLocalHint(t *testing.T) {
	for _, tc := range []struct {
		name string
		code string
		err  error
	}{
		{name: "база недоступна", err: ports.ErrCityUnavailable},
		{name: "город не определён"},
		{name: "регион неизвестен", code: "RU-UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detector := &fake.Cities{
				Detection: ports.DetectedCity{RegionCode: tc.code},
				Error:     tc.err,
			}
			service := app.NewFeatures(nil, detector, nil)
			result, err := service.Region(context.Background(), "", "192.0.2.1")
			if err != nil || result != (app.RegionView{Source: "none"}) || len(detector.IPs) != 1 {
				t.Fatal(result, detector.IPs, err)
			}
		})
	}
}
