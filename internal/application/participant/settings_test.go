package participant_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

var w3SettingsDelivery = participant.DeliverySettings{Regions: []string{"RU-MOW"}, Carriers: []string{"cdek"}}

func TestSettingsSync(t *testing.T) {
	store, clock, service := setup(t)
	ctx := context.Background()
	store.SeedTenant(participant.TenantState{
		TenantID: "t2",
		Slug:     "shop-two",
		Status:   participant.TenantActive,
		EventAt:  t0,
		Version:  1,
	})

	for _, tenant := range []string{"t1", "t2"} {
		command := appparticipant.CreateCommand{TenantID: participantID(tenant), Name: "Магазин " + tenant}
		if _, err := service.Create(ctx, command); err != nil {
			t.Fatal(err)
		}
	}

	platform := fake.NewPlatform()
	subsidy := participant.Subsidy{
		Enabled:   true,
		Type:      participant.SubsidyFixed,
		Value:     15000,
		MaxAmount: money.FromKopecks(20000),
	}
	platform.SetSettings("t1", ports.ShopSettings{
		Profile:  participant.Profile{Name: "Магазин t1"},
		Subsidy:  subsidy,
		Delivery: w3SettingsDelivery,
	})
	platform.Fail("t2", errors.New("сервер недоступен"))

	settings := appparticipant.NewSettings(store, platform, clock, &fake.IDs{})

	// задачи создания участников уже отработаны: иначе новая склеилась бы с невзятой
	tasks, err := store.Claim(
		ctx,
		ports.ClaimRequest{Now: clock.Now(), Limit: 100, PerTenant: 100, LeaseUntil: clock.Now()},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if err := store.Complete(ctx, task.ID, clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	before := len(store.OutboxRows())

	result, err := settings.Sync(ctx)
	if err != nil || result.Checked != 2 || result.Changed != 1 || len(result.Failed) != 1 ||
		result.Failed["t2"] == nil {
		t.Fatalf("сверка: %+v %v", result, err)
	}

	p, ok := store.Participant("t1")

	if !ok {
		t.Fatal("участник не найден")
	}
	if !participant.Restore(p).DeliveryValid() || p.Subsidy != subsidy || p.SettingsSyncedAt.IsZero() {
		t.Fatalf("настройки магазина: %+v", p)
	}

	rows := store.OutboxRows()
	if len(rows) == before {
		t.Fatal("смена доставки без смены имени ставит переиндексацию магазина")
	}
	if last := rows[len(rows)-1]; last.Kind != appcatalog.TaskSyncTenant || last.TenantID != "t1" {
		t.Fatalf("задача переиндексации: %+v", last)
	}

	// Удаляем ожидающие задачи, чтобы дедуп не маскировал лишнюю постановку.
	tasks, err = store.Claim(ctx, ports.ClaimRequest{
		Now:        clock.Now(),
		Limit:      100,
		PerTenant:  100,
		LeaseUntil: clock.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if err := store.Complete(ctx, task.ID, clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	before = len(store.OutboxRows())
	again, err := settings.Sync(ctx)
	if err != nil || again.Changed != 0 {
		t.Fatalf("без изменений версия не растёт: %+v %v", again, err)
	}
	if len(store.OutboxRows()) != before {
		t.Fatal("повторная сверка поставила задачи")
	}
	if p, ok := store.Participant("t1"); !ok || p.Version != 2 {
		t.Fatalf("версия участника: %d", p.Version)
	}
}

func TestInvalidParticipationSettingsHideSeller(t *testing.T) {
	store, clock, service := setup(t)
	ctx := context.Background()
	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: "Магазин"}); err != nil {
		t.Fatal(err)
	}
	platform := fake.NewPlatform()
	platform.SetSettings("t1", ports.ShopSettings{
		Subsidy:  participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
		Delivery: participant.DeliverySettings{Regions: []string{"RU-MOW"}, Carriers: []string{"cdek"}},
	})
	syncer := appparticipant.NewSettings(store, platform, clock, &fake.IDs{})
	if _, err := syncer.SyncOne(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	platform.Fail("t1", ports.ErrInvalidSettings)
	if _, err := syncer.SyncOne(ctx, "t1"); !errors.Is(err, ports.ErrInvalidSettings) {
		t.Fatal(err)
	}
	p, ok := store.Participant("t1")
	if !ok {
		t.Fatal("участник не найден")
	}
	if participant.Restore(p).DeliveryValid() {
		t.Fatal("некорректные обязательные настройки должны скрыть участника")
	}
}

func TestFailedSettingsPreserveCardAndModeration(t *testing.T) {
	for _, failure := range []error{ports.ErrInvalidSettings, ports.ErrMarketplaceDisabled} {
		t.Run(failure.Error(), func(t *testing.T) {
			store, service := seedStoreCard(t)
			ctx := context.Background()
			platform := fake.NewPlatform()
			settings := ports.ShopSettings{
				Profile:  participant.Profile{Name: "Магазин", Slogan: "Исходный слоган"},
				Card:     participant.CardReadiness{Ready: true},
				Returns:  &participant.Returns{Days: 14, Shipping: "Условия", How: "Инструкция"},
				Delivery: w3SettingsDelivery,
			}
			platform.Fail("t1", nil)
			platform.SetSettings("t1", settings)
			syncer := appparticipant.NewSettings(store, platform, fake.NewClock(t0), &fake.IDs{})
			if _, err := syncer.SyncOne(ctx, "t1"); err != nil {
				t.Fatal(err)
			}
			if _, err := service.ModerateCard(ctx, "t1", participant.CardSlogan, true, participant.Decision{
				Note: "  Причина  ",
			}); err != nil {
				t.Fatal(err)
			}
			before, ok := store.Participant("t1")
			if !ok {
				t.Fatal("участник не найден")
			}
			platform.Fail("t1", failure)
			_, err := syncer.SyncOne(ctx, "t1")
			if errors.Is(failure, ports.ErrInvalidSettings) && !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if errors.Is(failure, ports.ErrMarketplaceDisabled) && err != nil {
				t.Fatal(err)
			}
			after, ok := store.Participant("t1")
			if !ok {
				t.Fatal("участник не найден")
			}
			if !reflect.DeepEqual(before.StoreCard, after.StoreCard) {
				t.Fatal(before.StoreCard, after.StoreCard)
			}
			journal, err := service.ListDecisions(ctx, "t1", participant.Page{})
			if err != nil || journal.Total != 1 {
				t.Fatal(journal, err)
			}
			if journal.Items[0].PreviousValue != "Исходный слоган" || journal.Items[0].Note != "Причина" {
				t.Fatal(journal.Items)
			}
			platform.Fail("t1", nil)
			platform.SetSettings("t1", settings)
			if _, err := syncer.SyncOne(ctx, "t1"); err != nil {
				t.Fatal(err)
			}
			if after, ok = store.Participant("t1"); !ok {
				t.Fatal("участник t1 не найден")
			}
			if len(after.StoreCard.Hidden) != 1 {
				t.Fatal(after.StoreCard)
			}
			settings.Profile.Slogan = "Исправленный слоган"
			platform.Fail("t1", nil)
			platform.SetSettings("t1", settings)
			if _, err := syncer.SyncOne(ctx, "t1"); err != nil {
				t.Fatal(err)
			}
			journal, err = service.ListDecisions(ctx, "t1", participant.Page{})
			if err != nil || journal.Total != 2 {
				t.Fatal(journal, err)
			}
			found := false
			for _, record := range journal.Items {
				if record.Action == participant.ActionCardShow {
					found = record.Actor == participant.ActorSystem && record.PreviousValue == "Исходный слоган"
				}
			}
			if !found {
				t.Fatal(journal.Items)
			}
		})
	}
}

func TestCardOnlySettingsDoNotReindex(t *testing.T) {
	store, _, _ := setup(t)
	store.SeedParticipant(participant.State{
		TenantID:  "t1",
		Slug:      "shop",
		Name:      "Магазин",
		Stage:     participant.StageCandidate,
		Version:   1,
		StoreCard: participant.StoreCard{Profile: participant.Profile{Name: "Магазин"}},
	})
	platform := fake.NewPlatform()
	platform.SetSettings("t1", ports.ShopSettings{
		Profile: participant.Profile{Name: strings.Repeat("я", 201), Slogan: "Новый слоган"},
		Card:    participant.CardReadiness{Missing: []string{"terms"}},
	})
	syncer := appparticipant.NewSettings(store, platform, fake.NewClock(t0), &fake.IDs{})
	if changed, err := syncer.SyncOne(context.Background(), "t1"); err != nil || !changed {
		t.Fatal(changed, err)
	}
	state, ok := store.Participant("t1")
	if !ok {
		t.Fatal("участник не найден")
	}
	if state.Name != "Магазин" || state.StoreCard.Profile.Slogan != "Новый слоган" {
		t.Fatal(state)
	}
	for _, row := range store.OutboxRows() {
		if row.Kind == appcatalog.TaskSyncTenant {
			t.Fatal("смена карточки поставила переиндексацию")
		}
	}
	if len(store.OutboxRows()) != 1 || store.OutboxRows()[0].Kind != ports.TaskStoreCard {
		t.Fatal(store.OutboxRows())
	}
}
