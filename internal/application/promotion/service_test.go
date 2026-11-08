package promotion_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	app "github.com/proshop-app/proshop-marketplace/internal/application/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type environment struct {
	store   *fake.Store
	clock   *fake.Clock
	service *app.Service
	index   *fake.PromotionIndex
}

func setup() environment {
	store := fake.NewStore()
	clock := fake.NewClock(time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
	index := &fake.PromotionIndex{Index: fake.NewIndex()}
	service := app.NewService(store, clock, &fake.IDs{}, index, app.Config{
		Location:    time.UTC,
		SearchDepth: 100,
		VisitorSalt: "test-visitor-salt-012345",
		Random:      fake.Random{},
	})
	store.SeedParticipant(participant.State{
		StoreCard: participant.StoreCard{Readiness: participant.CardReadiness{Ready: true}},
		TenantID:  "t1",
		Slug:      "t1",
		Name:      "Магазин",
		Enabled:   true,
		Stage:     participant.StageConnected,
		Delivery:  participant.DeliverySettings{Regions: []string{"RU-AD"}, Carriers: []string{"cdek"}},
		Subsidy:   participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
		Version:   1,
	})
	store.SeedTenant(participant.TenantState{TenantID: "t1", Slug: "t1", Status: participant.TenantActive, Version: 1})
	for _, product := range []ids.ProductID{"p1", "p2"} {
		store.SeedProduct(catalog.ProductState{
			TenantID:  "t1",
			ProductID: product,
			Card: catalog.Card{
				Name:        "Товар",
				Visible:     true,
				Marketplace: &taxonomy.Card{CategoryID: "c1", Published: true},
				Publication: taxonomy.Publication{Published: true, CategoryID: "c1"},
			},
			PriceProfiles: []catalog.PriceProfile{{
				Key:            catalog.DefaultPriceProfile,
				Price:          money.FromKopecks(100),
				EffectivePrice: money.FromKopecks(100),
			}},
			Version: 1,
		})
		store.SeedVerdict(moderation.VerdictState{
			TenantID:  "t1",
			ProductID: product,
			Status:    moderation.Allowed,
			Version:   1,
		})
	}
	for _, code := range []string{"category_slot", "search_slot"} {
		scope := promotion.Category
		if code == "search_slot" {
			scope = promotion.Global
		}
		store.SeedPromotionPlacement(promotion.PlacementState{
			Code:         code,
			Surface:      code,
			Positions:    []int{1, 5},
			Scope:        scope,
			SlotLimit:    1,
			DefaultPrice: money.FromKopecks(700),
			Active:       true,
			Version:      1,
		})
	}
	return environment{store: store, clock: clock, service: service, index: index}
}

func (e environment) create(t *testing.T, code string) promotion.CampaignState {
	t.Helper()
	input := promotion.CampaignState{
		TenantID:      "t1",
		Kind:          promotion.SponsoredSlot,
		Marking:       promotion.Marking{Advertiser: "Рекламодатель", INN: "1234567890"},
		PlacementCode: code,
		Position:      1,
		Target:        promotion.Target{OfferID: ids.OfferID{Tenant: "t1", Product: "p1"}},
		Period:        promotion.Period{From: "2026-11-09", To: "2026-11-15"},
	}
	access := app.Access{TenantID: "t1", Actor: promotion.ActorTenant}
	result, err := e.service.Create(context.Background(), access, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Price.Kopecks() != 700 || result.Target.CategoryID != "c1" {
		t.Fatal(result)
	}
	return result.CampaignState
}

func (e environment) decide(t *testing.T, c promotion.CampaignState, action promotion.Action) promotion.CampaignState {
	t.Helper()
	command := app.Command{
		Action:          action,
		ExpectedVersion: c.Version,
		ERID:            "erid",
		Payment:         promotion.Payment{Amount: money.FromKopecks(700), PaidOn: "2026-11-06"},
	}
	result, err := e.service.Decide(context.Background(), app.Access{Actor: promotion.ActorOperator}, c.ID, command)
	if err != nil {
		t.Fatal(err)
	}
	return result.CampaignState
}

func TestCapacityApprovalAndTenant(t *testing.T) {
	e := setup()
	ctx := context.Background()
	first := e.decide(t, e.create(t, "category_slot"), promotion.Submit)
	second := e.create(t, "category_slot")
	_, err := e.service.Decide(ctx, app.Access{Actor: promotion.ActorTenant, TenantID: "t1"}, second.ID, app.Command{
		Action:          promotion.Submit,
		ExpectedVersion: second.Version,
	})
	if !errors.Is(err, promotion.ErrNoCapacity) {
		t.Fatal(err)
	}
	unchanged, err := e.service.Get(ctx, app.Access{}, second.ID)
	if err != nil || unchanged.Status != promotion.Draft {
		t.Fatal(unchanged, err)
	}
	_, err = e.service.Decide(ctx, app.Access{Actor: promotion.ActorOperator}, first.ID, app.Command{
		Action:          promotion.Approve,
		ExpectedVersion: first.Version,
	})
	if !errors.Is(err, promotion.ErrERIDRequired) {
		t.Fatal(err)
	}
	if _, err := e.service.Get(ctx, app.Access{TenantID: "t2"}, first.ID); !errors.Is(err, promotion.ErrNotFound) {
		t.Fatal(err)
	}
	e.decide(t, first, promotion.Stop)
	e.decide(t, second, promotion.Submit)
}

func TestActivationCountersAndSystemStop(t *testing.T) {
	e := setup()
	ctx := context.Background()
	c := e.decide(t, e.decide(t, e.create(t, "category_slot"), promotion.Submit), promotion.Approve)
	e.clock.Advance(72 * time.Hour)
	if err := e.service.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	unchanged, err := e.service.Get(ctx, app.Access{}, c.ID)
	if err != nil || unchanged.Status != promotion.Approved {
		t.Fatal(unchanged, err)
	}
	c = e.decide(t, c, promotion.Pay)
	if err := e.service.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	events := []app.Event{
		{PromotionID: c.ID, Kind: "view"},
		{PromotionID: c.ID, Kind: "view"},
		{PromotionID: c.ID, Kind: "click"},
	}
	for range 2 {
		if err := e.service.Events(ctx, "visitor", events); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := e.service.Stats(ctx, app.Access{TenantID: "t1"}, c.ID, "2026-11-09", "2026-11-15")
	if err != nil || len(rows) != 1 || rows[0].Impressions != 1 || rows[0].Clicks != 1 {
		t.Fatal(rows, err)
	}
	e.clock.Advance(24 * time.Hour)
	if err := e.service.Events(ctx, "visitor", events); err != nil {
		t.Fatal(err)
	}
	rows, err = e.service.Stats(ctx, app.Access{}, c.ID, "2026-11-09", "2026-11-15")
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	e.store.SeedVerdict(moderation.VerdictState{
		TenantID:  "t1",
		ProductID: "p1",
		Status:    moderation.Rejected,
		Version:   2,
	})
	if err := e.service.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	stopped, err := e.service.Get(ctx, app.Access{}, c.ID)
	if err != nil || stopped.Status != promotion.Stopped {
		t.Fatal(stopped, err)
	}
	decisions, err := e.service.Decisions(ctx, app.Access{}, c.ID)
	if err != nil || decisions[len(decisions)-1].Actor != promotion.ActorSystem {
		t.Fatal(decisions, err)
	}
}

func TestSearchRelevanceAndRotation(t *testing.T) {
	e := setup()
	ctx := context.Background()
	first := e.create(t, "search_slot")
	first.Status = promotion.Active
	second := first
	second.ID = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	second.Target.OfferID.Product = "p2"
	e.store.SeedPromotion(first)
	e.store.SeedPromotion(second)
	e.clock.Advance(72 * time.Hour)
	e.index.Result = ports.OfferPage{Items: []ports.OfferDocument{{ID: second.Target.OfferID.String()}}}
	query := ports.OfferQuery{Text: "Товар", TenantIDs: []string{"t1"}, CategoryID: "c1", InStock: true, Limit: 20}
	rows, err := e.service.Sponsored(ctx, query)
	if err != nil || len(rows) != 1 || rows[0].PromotionID != second.ID {
		t.Fatal(rows, err)
	}
	if e.index.Query.Limit != 100 || e.index.Query.Offset != 0 || e.index.Query.CategoryID != query.CategoryID ||
		len(e.index.Query.TenantIDs) != 1 || e.index.Query.TenantIDs[0] != "t1" || !e.index.Query.InStock {
		t.Fatal(e.index.Query)
	}
	query.Offset = 20
	rows, err = e.service.Sponsored(ctx, query)
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	e.index.Result.Items = append(e.index.Result.Items, ports.OfferDocument{ID: first.Target.OfferID.String()})
	query.Offset = 0
	rows, err = e.service.Sponsored(ctx, query)
	if err != nil || len(rows) != 1 || rows[0].PromotionID != first.ID {
		t.Fatal(rows, err)
	}
}

func TestCommitRollback(t *testing.T) {
	e := setup()
	c := e.create(t, "category_slot")
	e.store.FailCommit = errors.New("отказ фиксации")
	_, err := e.service.Decide(context.Background(), app.Access{Actor: promotion.ActorOperator}, c.ID, app.Command{
		Action:          promotion.Submit,
		ExpectedVersion: c.Version,
	})
	if err == nil {
		t.Fatal("ожидался откат")
	}
	e.store.FailCommit = nil
	state, err := e.service.Get(context.Background(), app.Access{}, c.ID)
	if err != nil || state.Status != promotion.Draft || state.Version != c.Version {
		t.Fatal(state, err)
	}
	decisions, err := e.service.Decisions(context.Background(), app.Access{}, c.ID)
	if err != nil || len(decisions) != 0 {
		t.Fatal(decisions, err)
	}
}

func TestCalendarOverviewAndPeriodEdit(t *testing.T) {
	e := setup()
	ctx := context.Background()
	c := e.decide(t, e.create(t, "category_slot"), promotion.Submit)
	calendar, err := e.service.Calendar(ctx, "category_slot", "2026-11-09", "2026-11-15", "")
	if err != nil || len(calendar.Days) != 7 || calendar.Days[0].Sold != 1 || calendar.Days[0].Limit != nil {
		t.Fatal(calendar, err)
	}
	scoped, err := e.service.Calendar(ctx, "category_slot", "2026-11-09", "2026-11-15", "c1")
	if err != nil || *scoped.Days[0].Positions[0].Limit != 1 || *scoped.Days[0].Limit != 2 {
		t.Fatal(scoped, err)
	}
	other := e.create(t, "category_slot")
	other.Period = promotion.Period{From: "2026-11-16", To: "2026-11-22"}
	edited, err := e.service.Edit(ctx, app.Access{Actor: promotion.ActorOperator}, other.ID, other.Version, other)
	if err != nil {
		t.Fatal(err)
	}
	other = e.decide(t, edited.CampaignState, promotion.Submit)
	other.Period = c.Period
	_, err = e.service.Edit(ctx, app.Access{Actor: promotion.ActorOperator}, other.ID, other.Version, other)
	if !errors.Is(err, promotion.ErrConflict) {
		t.Fatal(err)
	}
	c.Status = promotion.Active
	e.store.SeedPromotion(c)
	report, err := e.service.Overview(ctx, "2026-11-09", "2026-11-15")
	if err != nil || report.RevenueKopecks != 700 || report.ActiveCampaigns != 1 {
		t.Fatal(report, err)
	}
	if len(report.RevenueByDay) != 7 || report.RevenueByDay[0].Kopecks != 100 {
		t.Fatal(report)
	}
	availability, err := e.service.Availability(ctx, "category_slot", 1, "2026-11-09", 1, "c1")
	if err != nil || availability.Weeks[0].Free != "none" || availability.TotalKopecks != 700 {
		t.Fatal(availability, err)
	}
	free, err := e.service.Availability(ctx, "category_slot", 1, "2026-11-09", 1, "c2")
	if err != nil || free.Weeks[0].Free != "one" {
		t.Fatal(free, err)
	}
}

func TestReviseResubmitCapacityAndHistory(t *testing.T) {
	e := setup()
	ctx := context.Background()
	access := app.Access{TenantID: "t1", Actor: promotion.ActorTenant}
	c := e.decide(t, e.create(t, "category_slot"), promotion.Submit)
	view, err := e.service.Decide(ctx, app.Access{Actor: promotion.ActorOperator}, c.ID, app.Command{
		Action:          promotion.Reject,
		ExpectedVersion: c.Version,
		Note:            "Исправить реквизиты",
	})
	if err != nil {
		t.Fatal(err)
	}
	c = view.CampaignState
	view, err = e.service.Decide(ctx, access, c.ID, app.Command{Action: promotion.Revise, ExpectedVersion: c.Version})
	c = view.CampaignState
	if err != nil || c.Status != promotion.Draft || c.RejectionReason != "" {
		t.Fatal(c, err)
	}
	history, err := e.service.Decisions(ctx, access, c.ID)
	if err != nil || len(history) != 3 || history[1].Note != "Исправить реквизиты" {
		t.Fatal(history, err)
	}
	other := e.decide(t, e.create(t, "category_slot"), promotion.Submit)
	_, err = e.service.Decide(ctx, access, c.ID, app.Command{Action: promotion.Submit, ExpectedVersion: c.Version})
	if !errors.Is(err, promotion.ErrNoCapacity) {
		t.Fatal(err)
	}
	e.decide(t, other, promotion.Stop)
	c = e.decide(t, c, promotion.Submit)
	_, err = e.service.Decide(ctx, access, c.ID, app.Command{Action: promotion.Refund, ExpectedVersion: c.Version})
	if !errors.Is(err, promotion.ErrActionUnsupported) {
		t.Fatal(err)
	}
}

func TestAvailabilityMany(t *testing.T) {
	e := setup()
	_, err := e.service.ConfigurePlacement(
		context.Background(),
		"category_slot",
		3,
		money.FromKopecks(700),
		nil,
		true,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := e.service.Availability(context.Background(), "category_slot", 1, "2026-11-09", 1, "c1")
	if err != nil || len(result.Weeks) != 1 || result.Weeks[0].Free != "many" {
		t.Fatal(result, err)
	}
}

func TestM12ExpiryAndFinish(t *testing.T) {
	for _, status := range []promotion.Status{promotion.Review, promotion.Approved, promotion.Active} {
		t.Run(string(status), func(t *testing.T) {
			e := setup()
			c := e.create(t, "category_slot")
			c.Status = status
			e.store.SeedPromotion(c)
			e.clock.Advance(10 * 24 * time.Hour)
			if err := e.service.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, err := e.service.Get(context.Background(), app.Access{}, c.ID)
			want := promotion.Stopped
			if status == promotion.Active {
				want = promotion.Finished
			}
			if err != nil || got.Status != want {
				t.Fatal(got, err)
			}
			history, err := e.service.Decisions(context.Background(), app.Access{}, c.ID)
			if err != nil || len(history) != 1 || history[0].Actor != promotion.ActorSystem {
				t.Fatal(history, err)
			}
			if want == promotion.Stopped && history[0].Note != "период прошёл без оплаты/решения" {
				t.Fatal(history)
			}
			if err := e.service.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			history, err = e.service.Decisions(context.Background(), app.Access{}, c.ID)
			if err != nil || len(history) != 1 {
				t.Fatal(history, err)
			}
		})
	}
}

func TestM12EditsPaymentAndDelete(t *testing.T) {
	e := setup()
	ctx := context.Background()
	c := e.create(t, "category_slot")
	for _, action := range []promotion.Action{promotion.Pay, promotion.Finish} {
		_, err := e.service.Decide(ctx, app.Access{}, c.ID, app.Command{Action: action, ExpectedVersion: c.Version})
		if !errors.Is(err, promotion.ErrConflict) {
			t.Fatal(action, err)
		}
	}
	if err := e.service.Delete(ctx, app.Access{TenantID: "t2"}, c.ID); !errors.Is(err, promotion.ErrNotFound) {
		t.Fatal(err)
	}
	c = e.decide(t, c, promotion.Submit)
	if err := e.service.Delete(ctx, app.Access{}, c.ID); !errors.Is(err, promotion.ErrConflict) {
		t.Fatal(err)
	}
	c = e.decide(t, c, promotion.Approve)
	changed := c
	changed.Position = 5
	if _, err := e.service.Edit(ctx, app.Access{}, c.ID, c.Version, changed); !errors.Is(err, promotion.ErrConflict) {
		t.Fatal(err)
	}
	_, tenantErr := e.service.Edit(ctx, app.Access{TenantID: "t1"}, c.ID, c.Version, c)
	if !errors.Is(tenantErr, promotion.ErrConflict) {
		t.Fatal(tenantErr)
	}
	other := e.create(t, "category_slot")
	draft := e.create(t, "search_slot")
	_, err := e.service.ConfigurePlacement(ctx, "category_slot", 1, money.FromKopecks(900), nil, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	changed = c
	changed.Title = "Новое название"
	result, err := e.service.Edit(ctx, app.Access{}, c.ID, c.Version, changed)
	if err != nil || result.Price != c.Price {
		t.Fatal(result, err)
	}
	_, err = e.service.ConfigurePlacement(ctx, "category_slot", 2, money.FromKopecks(900), nil, true, 1)
	if !errors.Is(err, promotion.ErrConflict) {
		t.Fatal(err)
	}
	// Даже правка реквизитов одобренного размещения проверяет текущую ёмкость.
	other.Status = promotion.Review
	e.store.SeedPromotion(other)
	changed = result.CampaignState
	changed.Title = "Ещё одно название"
	_, err = e.service.Edit(ctx, app.Access{}, c.ID, result.Version, changed)
	if !errors.Is(err, promotion.ErrNoCapacity) {
		t.Fatal(err)
	}
	if err := e.service.Delete(ctx, app.Access{TenantID: "t1"}, draft.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Get(ctx, app.Access{}, draft.ID); !errors.Is(err, promotion.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestM12ListCountsFillAndNextMonday(t *testing.T) {
	e := setup()
	ctx := context.Background()
	first := e.create(t, "category_slot")
	e.clock.Advance(time.Hour)
	second := e.create(t, "category_slot")
	second.Status = promotion.Review
	second.Target.OfferID.Product = "p2"
	e.store.SeedPromotion(second)
	page, total, err := e.service.List(ctx, app.Access{TenantID: "t1"}, promotion.Filter{Limit: 1})
	if err != nil || total != 2 || len(page.Items) != 1 || page.Items[0].ID != second.ID {
		t.Fatal(page, total, err)
	}
	if page.Counts[promotion.Draft] != 1 || page.Counts[promotion.Review] != 1 || len(page.Counts) != 7 {
		t.Fatal(page.Counts)
	}
	page, total, err = e.service.List(ctx, app.Access{TenantID: "t1"}, promotion.Filter{ProductID: "p1"})
	if err != nil || total != 1 || page.Items[0].ID != first.ID || page.Limit != 50 {
		t.Fatal(page, total, err)
	}
	first.Target.CategoryID = "c2"
	e.store.SeedPromotion(first)
	report, err := e.service.Overview(ctx, "2026-11-09", "2026-11-15")
	if err != nil || report.Placements[0].Total != 2 {
		t.Fatal(report, err)
	}
	if e.service.NextMonday() != "2026-11-09" {
		t.Fatal(e.service.NextMonday())
	}
	e.clock.Advance(72 * time.Hour)
	if e.service.NextMonday() != "2026-11-16" {
		t.Fatal(e.service.NextMonday())
	}
}

func TestM12EventsLimitUnknownAndCategory(t *testing.T) {
	e := setup()
	ctx := context.Background()
	limited := app.NewService(e.store, e.clock, &fake.IDs{}, e.index, app.Config{
		Location:        time.UTC,
		Random:          fake.Random{},
		Limiter:         fake.NewLimiter(),
		EventsPerMinute: 1,
	})
	if err := limited.AllowEvents(ctx, "address1", 1); err != nil {
		t.Fatal(err)
	}
	if err := limited.AllowEvents(ctx, "address1", 1); !errors.Is(err, promotion.ErrRateLimited) {
		t.Fatal(err)
	}
	if err := limited.AllowEvents(ctx, "address2", 1); err != nil {
		t.Fatal(err)
	}
	events := []app.Event{
		{PromotionID: "not-uuid", Kind: "click"},
		{PromotionID: "ffffffff-ffff-ffff-ffff-ffffffffffff", Kind: "view"},
	}
	if err := e.service.Events(ctx, "visitor", events); err != nil {
		t.Fatal(err)
	}
	c := e.create(t, "category_slot")
	c.Status = promotion.Active
	c.Target.CategoryID = "old-category"
	e.store.SeedPromotion(c)
	e.clock.Advance(72 * time.Hour)
	rows, err := e.service.Sponsored(ctx, ports.OfferQuery{CategoryID: "old-category"})
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	if err := e.service.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := e.service.Get(ctx, app.Access{}, c.ID)
	if err != nil || got.Status != promotion.Stopped {
		t.Fatal(got, err)
	}
}

func TestM12RandomRequired(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("ожидалась ошибка сборки без Random")
		}
	}()
	app.NewService(fake.NewStore(), fake.NewClock(time.Now()), &fake.IDs{}, fake.NewIndex(), app.Config{})
}

func TestSubmitRequiresReadyStoreCard(t *testing.T) {
	e := setup()
	state, ok := e.store.Participant("t1")
	if !ok {
		t.Fatal("участник t1 не найден")
	}
	state.StoreCard.Readiness.Ready = false
	e.store.SeedParticipant(state)
	c := e.create(t, "category_slot")
	_, err := e.service.Decide(context.Background(), app.Access{TenantID: "t1"}, c.ID, app.Command{
		Action:          promotion.Submit,
		ExpectedVersion: c.Version,
	})
	if !errors.Is(err, promotion.ErrCardNotReady) {
		t.Fatal(err)
	}
}
