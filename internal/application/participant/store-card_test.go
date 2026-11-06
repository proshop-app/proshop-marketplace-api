package participant_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	app "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func seedStoreCard(t *testing.T) (*fake.Store, *app.Service) {
	t.Helper()
	store, _, service := setup(t)
	store.SeedParticipant(participant.State{
		TenantID: "t1",
		Slug:     "shop-one",
		Name:     "Магазин",
		Stage:    participant.StageConnected,
		Version:  1,
		Delivery: w3SettingsDelivery,
		Subsidy:  participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
		StoreCard: participant.StoreCard{
			Readiness: participant.CardReadiness{Ready: true},
			Profile: participant.Profile{
				Showcase: participant.Showcase{Mode: "pinned", ProductIDs: []string{"p3", "gone"}},
			},
		},
	})
	store.SeedTaxonomy(taxonomy.Definition{Categories: []taxonomy.Category{{
		ID:       "root",
		Name:     "Раздел",
		Children: []taxonomy.Category{{ID: "leaf", Name: "Подраздел"}},
	}}})
	for i, id := range []ids.ProductID{"p1", "p2", "p3", "p4"} {
		price := money.FromKopecks(int64(100 + i))
		image := ""
		if id == "p1" {
			image = "image"
		}
		store.SeedProduct(catalog.ProductState{
			TenantID:  "t1",
			ProductID: id,
			Revision:  int64(i + 1),
			Version:   1,
			Card: catalog.Card{
				Name:        "Товар",
				Image:       image,
				Visible:     true,
				Marketplace: &taxonomy.Card{Published: true, CategoryID: "leaf"},
				Publication: taxonomy.Publication{Published: true, CategoryID: "leaf"},
			},
			PriceProfiles: []catalog.PriceProfile{{
				Key:            catalog.DefaultPriceProfile,
				Price:          money.FromKopecks(1000),
				EffectivePrice: price,
			}},
		})
		store.SeedVerdict(moderation.VerdictState{
			TenantID:  "t1",
			ProductID: id,
			Status:    moderation.Allowed,
			Version:   1,
		})
	}
	store.SeedProduct(catalog.ProductState{
		TenantID:  "t1",
		ProductID: "unknown",
		Card:      catalog.Card{Marketplace: &taxonomy.Card{Published: true, CategoryID: "absent"}},
	})
	return store, service
}

func TestStoreSummaryRootsPricesAndShowcase(t *testing.T) {
	store, service := seedStoreCard(t)
	ctx := context.Background()
	refreshStore(t, service)
	summary, err := service.StoreSummary(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Offers != 4 || summary.FromPrice == nil || *summary.FromPrice != 100 || summary.Uncategorized != 1 {
		t.Fatal(summary)
	}
	if len(summary.Categories) != 1 || summary.Categories[0].ID != "root" || summary.Categories[0].Count != 4 {
		t.Fatal(summary.Categories)
	}
	if len(summary.Showcase) != 3 || summary.Showcase[0].ProductID != "p3" ||
		summary.Showcase[1].ProductID != "p1" || summary.Showcase[2].ProductID != "p4" {
		t.Fatal(summary.Showcase)
	}
	store.SeedVerdict(moderation.VerdictState{TenantID: "t1", ProductID: "p3", Status: moderation.Rejected})
	refreshStore(t, service)
	summary, err = service.StoreSummary(ctx, "t1")
	if err != nil || summary.Showcase[0].ProductID != "p1" || summary.Offers != 3 {
		t.Fatal(summary, err)
	}
	header, err := service.Shop(ctx, "shop-one")
	if err != nil {
		t.Fatal(err)
	}
	pageWithShowcase, err := service.Shops(ctx, "", 20, 0)
	if err != nil || len(pageWithShowcase.Items) != 1 {
		t.Fatal(pageWithShowcase, err)
	}
	if len(header.Showcase) != 3 || !reflect.DeepEqual(header.Showcase, pageWithShowcase.Items[0].Showcase) {
		t.Fatal(header.Showcase, pageWithShowcase.Items[0].Showcase)
	}
	for _, item := range header.Showcase {
		if item.OfferID == (ids.OfferID{Tenant: "t1", Product: "p3"}).String() {
			t.Fatal("скрытое предложение", item)
		}
	}
	state, ok := store.Participant("t1")
	if !ok {
		t.Fatal("участник не найден")
	}
	state.Stage = participant.StageCandidate
	store.SeedParticipant(state)
	refreshStore(t, service)
	summary, err = service.StoreSummary(ctx, "t1")
	if err != nil || summary.Offers != 4 || len(summary.Showcase) != 3 {
		t.Fatal(summary, err)
	}
	refreshStore(t, service)
	page, err := service.Shops(ctx, "", 20, 0)
	if err != nil || page.Total != 0 {
		t.Fatal(page, err)
	}
}

func TestStoreCardSettingsAndJournal(t *testing.T) {
	store, service := seedStoreCard(t)
	ctx := context.Background()
	platform := fake.NewPlatform()
	settings := ports.ShopSettings{
		Profile:  participant.Profile{Name: "Имя сервера", Slogan: "Слоган"},
		Card:     participant.CardReadiness{Ready: true},
		Delivery: w3SettingsDelivery,
		Subsidy:  participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
	}
	platform.SetSettings("t1", settings)
	syncer := app.NewSettings(store, platform, fake.NewClock(t0), &fake.IDs{})
	if changed, err := syncer.SyncOne(ctx, "t1"); !changed || err != nil {
		t.Fatal(changed, err)
	}
	if state, ok := store.Participant("t1"); !ok || state.Name != settings.Profile.Name || state.Version != 2 {
		t.Fatal(state)
	}
	decision := participant.Decision{Note: "Причина"}
	if _, err := service.ModerateCard(ctx, "t1", "slogan", true, decision); err != nil {
		t.Fatal(err)
	}
	refreshStore(t, service)
	header, err := service.Shop(ctx, "shop-one")
	if err != nil || header.Slogan != "" {
		t.Fatal(header, err)
	}
	history, err := service.ListDecisions(ctx, "t1", participant.Page{})
	if err != nil || history.Total != 1 || history.Items[0].Action != "card_hide" {
		t.Fatal(history, err)
	}
	tasks := len(store.OutboxRows())
	settings.Profile.Slogan = "Исправлен"
	settings.Card.Ready = false
	platform.SetSettings("t1", settings)
	if _, err := syncer.SyncOne(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if len(store.OutboxRows()) != tasks {
		t.Fatal("профиль без смены имени поставил переиндексацию")
	}
	state, ok := store.Participant("t1")
	if !ok {
		t.Fatal("участник не найден")
	}
	if !state.Enabled || len(state.StoreCard.Hidden) != 0 {
		t.Fatal(state)
	}
	refreshStore(t, service)
	page, err := service.Shops(ctx, "", 20, 0)
	if err != nil || page.Total != 0 {
		t.Fatal(page, err)
	}
}

func TestStoreSalesPrecedeImageAndRevision(t *testing.T) {
	store, service := seedStoreCard(t)
	if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Products().Get(ctx, "t1", "p2")
		if err != nil {
			return err
		}
		state := p.State()
		snapshot := catalog.Snapshot{
			TenantID:      state.TenantID,
			ProductID:     state.ProductID,
			Revision:      state.Revision + 1,
			Card:          state.Card,
			PriceProfiles: state.PriceProfiles,
			SalesCount:    2,
			At:            t0,
		}
		if _, err := p.Apply(snapshot, t0); err != nil {
			return err
		}
		return tx.Products().Save(ctx, p)
	}); err != nil {
		t.Fatal(err)
	}
	refreshStore(t, service)
	summary, err := service.StoreSummary(context.Background(), "t1")
	if err != nil || summary.Showcase[1].ProductID != "p2" {
		t.Fatal(summary, err)
	}
	member, ok := store.Participant("t1")
	if !ok {
		t.Fatal("участник не найден")
	}
	member.StoreCard.Profile.Showcase.Mode = "auto"
	store.SeedParticipant(member)
	refreshStore(t, service)
	summary, err = service.StoreSummary(context.Background(), "t1")
	if err != nil || summary.Showcase[0].ProductID != "p2" || summary.Showcase[1].ProductID != "p1" {
		t.Fatal(summary, err)
	}
}

func TestShopsCategoryPagination(t *testing.T) {
	_, service := seedStoreCard(t)
	for _, category := range []string{"root", "leaf"} {
		refreshStore(t, service)
		page, err := service.Shops(context.Background(), category, 1, 0)
		if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].Category.ID != category {
			t.Fatal(page, err)
		}
		refreshStore(t, service)
		empty, err := service.Shops(context.Background(), category, 1, 1)
		if err != nil || empty.Total != 1 || len(empty.Items) != 0 {
			t.Fatal(empty, err)
		}
	}
}

func refreshStore(t *testing.T, service *app.Service) {
	t.Helper()
	task := ports.Task{Kind: ports.TaskStoreCard, TenantID: "t1"}
	if err := service.RefreshStoreCard(context.Background(), task); err != nil {
		t.Fatal(err)
	}
}

func TestStoreSummaryMultipleRootsAndNoVisibleOffers(t *testing.T) {
	store, service := seedStoreCard(t)
	store.SeedTaxonomy(taxonomy.Definition{Categories: []taxonomy.Category{
		{ID: "root", Name: "Б", Children: []taxonomy.Category{{ID: "leaf", Name: "Лист"}}},
		{ID: "second", Name: "А"},
	}})
	for _, id := range []ids.ProductID{"p1", "p2"} {
		if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
			p, err := tx.Products().Get(ctx, "t1", id)
			if err != nil {
				return err
			}
			state := p.State()
			state.Card.Publication.CategoryID = "second"
			storeProduct := catalog.RestoreProduct(state)
			return tx.Products().Save(ctx, storeProduct)
		}); err != nil {
			t.Fatal(err)
		}
	}
	refreshStore(t, service)
	summary, err := service.StoreSummary(context.Background(), "t1")
	if err != nil || len(summary.Categories) != 2 || summary.Categories[0].ID != "second" ||
		summary.Categories[0].Count != 2 || summary.Categories[1].Count != 2 {
		t.Fatal(summary, err)
	}
	for _, id := range []ids.ProductID{"p1", "p2", "p3", "p4"} {
		store.SeedVerdict(moderation.VerdictState{TenantID: "t1", ProductID: id, Status: moderation.Rejected})
	}
	refreshStore(t, service)
	page, err := service.Shops(context.Background(), "", 101, 0)
	if err != nil || page.Total != 0 || page.Limit != 100 {
		t.Fatal(page, err)
	}
}

func TestStoreSummaryCandidateSkipsMissingDefaultPrice(t *testing.T) {
	store, service := seedStoreCard(t)
	member, ok := store.Participant("t1")
	if !ok {
		t.Fatal("участник не найден")
	}
	member.Stage = participant.StageCandidate
	member.Name = "«!Магазин»"
	store.SeedParticipant(member)
	store.SeedProduct(catalog.ProductState{
		TenantID:  "t1",
		ProductID: "no-price",
		Card: catalog.Card{
			Name:        "Без цены",
			Marketplace: &taxonomy.Card{Published: true, CategoryID: "leaf"},
			Publication: taxonomy.Publication{Published: true, CategoryID: "leaf"},
		},
	})
	refreshStore(t, service)
	summary, err := service.StoreSummary(context.Background(), "t1")
	if err != nil || summary.Offers != 4 || *summary.FromPrice != 100 || len(summary.Showcase) != 3 {
		t.Fatal(summary, err)
	}
	if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		record, err := tx.StoreCards().Get(ctx, "t1")
		if err != nil {
			return err
		}
		if record.Header.Initial != "М" || record.Header.Color.Card != participant.NeutralCardColor {
			t.Fatal(record.Header)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreSummaryBeforeFirstRefresh(t *testing.T) {
	_, service := seedStoreCard(t)
	summary, err := service.StoreSummary(context.Background(), "t1")
	if err != nil || summary.Offers != 0 || summary.FromPrice != nil || summary.Categories == nil ||
		summary.Showcase == nil || summary.Hidden == nil ||
		summary.Rating != nil || summary.Badge != nil || summary.Rank != nil {
		t.Fatal(summary, err)
	}
	if _, err := service.Shop(context.Background(), "shop-one"); !errors.Is(err, participant.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := service.StoreSummary(context.Background(), "absent"); !errors.Is(err, participant.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestModerationUpdatesPublicSnapshotImmediately(t *testing.T) {
	store, service := seedStoreCard(t)
	state, ok := store.Participant("t1")
	if !ok {
		t.Fatal("участник не найден")
	}
	logo := "logo"
	state.StoreCard.Profile.Slogan = "Слоган"
	state.StoreCard.Profile.Logo = &logo
	store.SeedParticipant(state)
	refreshStore(t, service)
	ctx := context.Background()
	for _, key := range []string{participant.CardSlogan, participant.CardLogo} {
		for _, hide := range []bool{true, false} {
			if _, err := service.ModerateCard(ctx, "t1", key, hide, participant.Decision{Note: "Причина"}); err != nil {
				t.Fatal(err)
			}
			header, err := service.Shop(ctx, "shop-one")
			if err != nil {
				t.Fatal(err)
			}
			page, err := service.Shops(ctx, "", 20, 0)
			if err != nil || len(page.Items) != 1 {
				t.Fatal(page, err)
			}
			card := page.Items[0]
			if key == participant.CardSlogan && ((header.Slogan == "") != hide || (card.Slogan == "") != hide) {
				t.Fatal(header, card)
			}
			if key == participant.CardLogo && ((header.Logo == nil) != hide || (card.Logo == nil) != hide) {
				t.Fatal(header, card)
			}
			summary, err := service.StoreSummary(ctx, "t1")
			if err != nil || (len(summary.Hidden) == 1) != hide {
				t.Fatal(summary, err)
			}
		}
	}
}

func TestStoreSummaryFallbackForConnectedWithoutDelivery(t *testing.T) {
	store, service := seedStoreCard(t)
	state, ok := store.Participant("t1")
	if !ok {
		t.Fatal("участник не найден")
	}
	state.Delivery = participant.DeliverySettings{}
	store.SeedParticipant(state)
	refreshStore(t, service)
	summary, err := service.StoreSummary(context.Background(), "t1")
	if err != nil || summary.Offers != 4 || len(summary.Showcase) != 3 {
		t.Fatal(summary, err)
	}
	page, err := service.Shops(context.Background(), "", 20, 0)
	if err != nil || page.Total != 0 {
		t.Fatal(page, err)
	}
}

func TestStoreWeightedRatingOnlyVisibleOffers(t *testing.T) {
	store, service := seedStoreCard(t)
	member, _ := store.Participant("t1")
	member.Enabled = true
	store.SeedParticipant(member)
	ctx := context.Background()
	for i, id := range []ids.ProductID{"p1", "p2", "p3", "p4"} {
		if err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			p, err := tx.Products().Get(ctx, "t1", id)
			if err != nil {
				return err
			}
			state := p.State()
			value, count := 4.0, int64(3)
			if i == 0 || i == 3 {
				state.Card.VariantsID = "v1"
			}
			if i == 1 {
				value, count = 5, 1
			}
			if i == 2 {
				state.Card.Visible = false
				value, count = 1, 100
			}
			next := catalog.Snapshot{
				TenantID:      state.TenantID,
				ProductID:     state.ProductID,
				Revision:      state.Revision + 1,
				At:            t0,
				Card:          state.Card,
				PriceProfiles: state.PriceProfiles,
				AverageRating: value,
				RatingsCount:  count,
			}
			_, err = p.Apply(next, t0)
			if err != nil {
				return err
			}
			return tx.Products().Save(ctx, p)
		}); err != nil {
			t.Fatal(err)
		}
	}
	refreshStore(t, service)
	summary, err := service.StoreSummary(ctx, "t1")
	if err != nil || summary.Rating == nil || summary.Rating.Value != 4.3 || summary.Rating.Count != 4 {
		t.Fatal(summary, err)
	}
	header, err := service.Shop(ctx, member.Slug)
	if err != nil || header.Rating == nil || *header.Rating != *summary.Rating {
		t.Fatal(header, err)
	}
	page, err := service.Shops(ctx, "", 20, 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].Rating == nil ||
		*page.Items[0].Rating != *summary.Rating {
		t.Fatal(page, err)
	}
}

func TestShopPageWithoutReadyCard(t *testing.T) {
	store, service := seedStoreCard(t)
	state, _ := store.Participant("t1")
	state.StoreCard.Readiness.Ready = false
	store.SeedParticipant(state)
	refreshStore(t, service)
	ctx := context.Background()
	if _, err := service.Shop(ctx, "shop-one"); err != nil {
		t.Fatal(err)
	}
	if summary, err := service.StorePageSummary(ctx, "t1"); err != nil || summary.Offers != 4 {
		t.Fatal(summary, err)
	}
	for _, category := range []string{"", "root"} {
		page, err := service.Shops(ctx, category, 20, 0)
		if err != nil || page.Total != 0 || len(page.Items) != 0 {
			t.Fatal(page, err)
		}
	}
	for _, id := range []ids.ProductID{"p1", "p2", "p3", "p4"} {
		store.SeedVerdict(moderation.VerdictState{
			TenantID:  "t1",
			ProductID: id,
			Status:    moderation.Pending,
		})
	}
	refreshStore(t, service)
	if _, err := service.Shop(ctx, "shop-one"); !errors.Is(err, participant.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := service.StorePageSummary(ctx, "t1"); !errors.Is(err, participant.ErrNotFound) {
		t.Fatal(err)
	}
}
