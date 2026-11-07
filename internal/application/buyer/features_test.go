package buyer_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	app "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const featureBuyer ids.UUID = "7a1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b"

type trackedFeatureUOW struct {
	ports.UnitOfWork
	active bool
}

type checkedFeaturePricing struct {
	ports.CatalogPricing
	uow   *trackedFeatureUOW
	calls int
}

func (u *trackedFeatureUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return u.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		u.active = true
		defer func() { u.active = false }()
		return fn(ctx, tx)
	})
}

func (p *checkedFeaturePricing) PriceContexts(
	ctx context.Context,
	id ids.UUID,
	tenants []ids.TenantID,
) (map[ids.TenantID]string, error) {
	p.calls++
	if p.uow.active {
		return nil, errors.New("pricing вызван внутри транзакции")
	}
	return p.CatalogPricing.PriceContexts(ctx, id, tenants)
}

func seedFeatureOffer(store *fake.Store, price int64, visible bool) {
	store.SeedTenant(participant.TenantState{TenantID: "t1", Status: participant.TenantActive})
	store.SeedParticipant(participant.State{
		TenantID: "t1",
		Enabled:  true,
		Stage:    participant.StageConnected,
		Subsidy:  participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
		Delivery: participant.DeliverySettings{Regions: []string{"RU-AD"}, Carriers: []string{"cdek"}},
	})
	store.SeedProduct(catalog.ProductState{
		TenantID:  "t1",
		ProductID: "p1",
		Available: 5,
		Card:      catalog.Card{Name: "Товар", Visible: visible, Publication: taxonomy.Publication{Published: true}},
		PriceProfiles: []catalog.PriceProfile{{
			Key:            catalog.DefaultPriceProfile,
			Price:          money.FromKopecks(price),
			EffectivePrice: money.FromKopecks(price),
		}},
	})
	store.SeedVerdict(moderation.VerdictState{TenantID: "t1", ProductID: "p1", Status: moderation.Allowed})
}

func TestSavedPriceAndUnavailable(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	service := app.NewFeatures(store, nil, fake.NewPlatform(), fake.NewLimiter())
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
	if err != nil || (profile.Alerts.PriceDrops == nil || *profile.Alerts.PriceDrops != 1) {
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
	service := app.NewFeatures(store, nil, fake.NewPlatform(), fake.NewLimiter())
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
	geo := &fake.GeoIP{Region: &buyer.Region{Code: "RU-AD"}}
	service := app.NewFeatures(store, geo, fake.NewPlatform(), fake.NewLimiter())
	result, err := service.Region(ctx, featureBuyer, "192.0.2.1")
	if err != nil || result.Source != "ip" {
		t.Fatalf("IP: %+v %v", result, err)
	}
	err = store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p := buyer.RestoreProfile(buyer.ProfileState{})
		_, err := p.SelectPickup(buyer.Pickup{Provider: "cdek", Code: "p1", RegionCode: "RU-AD"})
		if err != nil {
			return err
		}
		return tx.BuyerFeatures().SaveProfile(ctx, featureBuyer, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err = service.Region(ctx, featureBuyer, "192.0.2.1")
	if err != nil || result.Source != "pickup" || geo.Calls != 1 {
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
	result, err = service.Region(ctx, featureBuyer, "192.0.2.1")
	if err != nil || result.Source != "profile" || *result.Code != "RU-AL" || geo.Calls != 1 {
		t.Fatal(result, err)
	}
	none, err := app.NewFeatures(store, nil, fake.NewPlatform(), fake.NewLimiter()).Region(ctx, "", "192.0.2.1")
	if err != nil || none.Source != "none" || none.Code != nil {
		t.Fatal(none, err)
	}
}

func TestSubscriptionsAreIdempotentAndCounted(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	service := app.NewFeatures(store, nil, fake.NewPlatform(), fake.NewLimiter())
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
	service := app.NewFeatures(store, nil, fake.NewPlatform(), fake.NewLimiter())
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
	service := app.NewFeatures(store, nil, fake.NewPlatform(), fake.NewLimiter())
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

func TestRegionProviderLimit(t *testing.T) {
	ctx := context.Background()
	geo := &fake.GeoIP{Region: &buyer.Region{Code: "RU-AD"}, CacheHit: true}
	limiter := fake.NewLimiter()
	service := app.NewFeatures(fake.NewStore(), geo, fake.NewPlatform(), limiter)
	for i := 0; i < 40; i++ {
		if result, err := service.Region(ctx, "", "192.0.2.1"); err != nil || result.Source != app.RegionIP {
			t.Fatal(result, err)
		}
	}
	if geo.Calls != 0 {
		t.Fatal("попадание в кэш вызвало провайдера")
	}
	geo.CacheHit = false
	for i := 0; i < 30; i++ {
		if _, err := service.Region(ctx, "", "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Region(ctx, "", "192.0.2.2"); !errors.Is(err, buyer.ErrRegionLimit) {
		t.Fatal(err)
	}
	if geo.Calls != 30 {
		t.Fatal(geo.Calls)
	}
	if _, err := service.Region(ctx, featureBuyer, "192.0.2.2"); err != nil {
		t.Fatal(err)
	}
	limiter.Err = errors.New("лимитер недоступен")
	if result, err := service.Region(ctx, "", "192.0.2.2"); err != nil || result.Source != app.RegionIP {
		t.Fatal(result, err)
	}
	if geo.Calls != 32 {
		t.Fatal(geo.Calls)
	}
}

func TestSavedPersonalPricesAndContextFailure(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	platform := fake.NewPlatform()
	service := app.NewFeatures(store, nil, platform, fake.NewLimiter())
	seedFeatureOffer(store, 100, true)
	state, _ := store.Product("t1", "p1")
	state.PriceProfiles = append(state.PriceProfiles, catalog.PriceProfile{
		Key:            "marketplace:personal",
		Price:          money.FromKopecks(200),
		EffectivePrice: money.FromKopecks(180),
	})
	store.SeedProduct(state)
	platform.SetBuyerProfile(featureBuyer, "t1", "marketplace:personal")
	offer := ids.OfferID{Tenant: "t1", Product: "p1"}
	for _, kind := range []ports.SavedKind{ports.Favorites, ports.PriceWatches} {
		if err := service.PutSaved(ctx, featureBuyer, kind, offer, nil); err != nil {
			t.Fatal(err)
		}
	}
	state.PriceProfiles[1].EffectivePrice = money.FromKopecks(150)
	store.SeedProduct(state)
	items, err := service.Saved(ctx, featureBuyer)
	if err != nil || len(items) != 1 || items[0].PriceAtWatch != 180 ||
		*items[0].CurrentPrice != 150 || !items[0].Dropped {
		t.Fatal(items, err)
	}
	favorites, _, err := service.Favorites(ctx, featureBuyer, 20, 0)
	if err != nil || favorites[0].Offer.Price != 150 || favorites[0].Offer.BasePrice != 200 {
		t.Fatal(favorites, err)
	}
	profile, err := service.Profile(ctx, featureBuyer)
	if err != nil || (profile.Alerts.PriceDrops == nil || *profile.Alerts.PriceDrops != 1) {
		t.Fatal(profile, err)
	}
	failure := errors.New("контексты недоступны")
	platform.Fail("t1", failure)
	store.SeedPickupPoint(delivery.PickupPoint{
		Carrier:    "cdek",
		Code:       "point-1",
		RegionCode: "RU-AD",
		Address:    "Адрес пункта",
		Active:     true,
	})
	if profile, err := service.Profile(ctx, featureBuyer); err != nil || profile.Alerts.PriceDrops != nil {
		t.Fatal(profile, err)
	}
	if _, err := service.Saved(ctx, featureBuyer); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, _, err := service.Favorites(ctx, featureBuyer, 20, 0); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if profile, err := service.Patch(ctx, featureBuyer, func(state buyer.ProfileState) (buyer.ProfileState, error) {
		state.Name = "Имя"
		state.Region = &buyer.Region{Code: "RU-AD"}
		state.PickupPoint = &buyer.Pickup{Provider: "cdek", Code: "point-1"}
		state.Notifications = buyer.Notifications{PriceDrops: true, ShopNews: true}
		return state, nil
	}); err != nil || profile.Name != "Имя" || profile.Alerts.PriceDrops != nil {
		t.Fatal(profile, err)
	}
	platform.Fail("t1", nil)
	profile, err = service.Profile(ctx, featureBuyer)
	if err != nil || profile.Name != "Имя" || profile.Region == nil || profile.Region.Code != "RU-AD" ||
		profile.PickupPoint == nil || profile.PickupPoint.Address != "Адрес пункта" ||
		!profile.Notifications.PriceDrops || !profile.Notifications.ShopNews {
		t.Fatal("PATCH не сохранился", profile, err)
	}
}

func TestFeaturePricingOutsideTransaction(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	seedFeatureOffer(store, 100, true)
	uow := &trackedFeatureUOW{UnitOfWork: store}
	pricing := &checkedFeaturePricing{CatalogPricing: fake.NewPlatform(), uow: uow}
	service := app.NewFeatures(uow, nil, pricing, fake.NewLimiter())
	if _, err := appcatalog.PriceContexts(ctx, uow, pricing, featureBuyer, nil); err != nil {
		t.Fatal(err)
	}
	offer := ids.OfferID{Tenant: "t1", Product: "p1"}
	for _, kind := range []ports.SavedKind{ports.Favorites, ports.PriceWatches} {
		if err := service.PutSaved(ctx, featureBuyer, kind, offer, nil); err != nil {
			t.Fatal(err)
		}
	}
	if profile, err := service.Profile(ctx, featureBuyer); err != nil || profile.Alerts.PriceDrops == nil {
		t.Fatal(profile, err)
	}
	profile, err := service.Patch(ctx, featureBuyer, func(state buyer.ProfileState) (buyer.ProfileState, error) {
		state.Name = "Имя"
		return state, nil
	})
	if err != nil || profile.Alerts.PriceDrops == nil {
		t.Fatal(profile, err)
	}
	if _, err := service.Saved(ctx, featureBuyer); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Favorites(ctx, featureBuyer, 20, 0); err != nil {
		t.Fatal(err)
	}
	if pricing.calls != 7 {
		t.Fatal(pricing.calls)
	}
	if _, err := service.Saved(ctx, "empty"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Favorites(ctx, featureBuyer, 20, 1); err != nil {
		t.Fatal(err)
	}
	if pricing.calls != 7 {
		t.Fatal("пустые списки запрашивают контексты", pricing.calls)
	}
}

func TestRegionLimitRetryHint(t *testing.T) {
	limiter := fake.NewLimiter()
	geo := &fake.GeoIP{}
	service := app.NewFeatures(fake.NewStore(), geo, fake.NewPlatform(), limiter)
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		if _, err := service.Region(ctx, "", "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	_, err := service.Region(ctx, "", "192.0.2.1")
	var limited *app.RegionLimitError
	if !errors.As(err, &limited) || limited.RetryAfter <= 0 || limited.RetryAfter > time.Minute {
		t.Fatal(err)
	}
}

func TestProfilePricingOnlyWatchedTenants(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	seedFeatureOffer(store, 100, true)
	store.SeedTenant(participant.TenantState{TenantID: "t2", Status: participant.TenantActive})
	store.SeedParticipant(participant.State{
		TenantID: "t2",
		Enabled:  true,
		Stage:    participant.StageConnected,
		Subsidy:  participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
		Delivery: participant.DeliverySettings{Regions: []string{"RU-AD"}, Carriers: []string{"cdek"}},
	})
	platform := fake.NewPlatform()
	platform.Fail("t2", errors.New("посторонний магазин недоступен"))
	uow := &trackedFeatureUOW{UnitOfWork: store}
	pricing := &checkedFeaturePricing{CatalogPricing: platform, uow: uow}
	service := app.NewFeatures(uow, nil, pricing, fake.NewLimiter())
	check := func(want int) {
		t.Helper()
		profile, err := service.Profile(ctx, featureBuyer)
		if err != nil || profile.Alerts.PriceDrops == nil || *profile.Alerts.PriceDrops != want {
			t.Fatal(profile, err)
		}
		profile, err = service.Patch(ctx, featureBuyer, func(state buyer.ProfileState) (buyer.ProfileState, error) {
			state.Name = "Имя"
			return state, nil
		})
		if err != nil || profile.Alerts.PriceDrops == nil || *profile.Alerts.PriceDrops != want {
			t.Fatal(profile, err)
		}
	}
	check(0)
	if pricing.calls != 0 {
		t.Fatal("пустой профиль вызвал платформу", pricing.calls)
	}
	offer := ids.OfferID{Tenant: "t1", Product: "p1"}
	if err := service.PutSaved(ctx, featureBuyer, ports.Favorites, offer, nil); err != nil {
		t.Fatal(err)
	}
	check(0)
	if pricing.calls != 1 {
		t.Fatal("избранное не является наблюдением", pricing.calls)
	}
	if err := service.PutSaved(ctx, featureBuyer, ports.PriceWatches, offer, nil); err != nil {
		t.Fatal(err)
	}
	seedFeatureOffer(store, 90, true)
	check(1)
	if pricing.calls != 4 {
		t.Fatal(pricing.calls)
	}
}
