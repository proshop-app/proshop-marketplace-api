package participant_test

import (
	"context"
	"errors"
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
	platform.SetSettings("t1", ports.ShopSettings{Subsidy: subsidy, Delivery: w3SettingsDelivery})
	platform.Fail("t2", errors.New("сервер недоступен"))

	settings := appparticipant.NewSettings(store, platform, clock, &fake.IDs{})

	// задачи создания участников уже отработаны: иначе новая склеилась бы с невзятой
	tasks, _ := store.Claim(
		ctx,
		ports.ClaimRequest{Now: clock.Now(), Limit: 100, PerTenant: 100, LeaseUntil: clock.Now()},
	)
	for _, task := range tasks {
		_ = store.Complete(ctx, task.ID, clock.Now())
	}
	before := len(store.OutboxRows())

	result, err := settings.Sync(ctx)
	if err != nil || result.Checked != 2 || result.Changed != 1 || len(result.Failed) != 1 ||
		result.Failed["t2"] == nil {
		t.Fatalf("сверка: %+v %v", result, err)
	}

	p, _ := store.Participant("t1")
	if !participant.Restore(p).DeliveryValid() || p.Subsidy != subsidy || p.SettingsSyncedAt.IsZero() {
		t.Fatalf("настройки магазина: %+v", p)
	}

	rows := store.OutboxRows()
	if len(rows) == before {
		t.Fatal("смена флага ставит переиндексацию магазина")
	}
	if last := rows[len(rows)-1]; last.Kind != appcatalog.TaskSyncTenant || last.TenantID != "t1" {
		t.Fatalf("задача переиндексации: %+v", last)
	}

	again, err := settings.Sync(ctx)
	if err != nil || again.Changed != 0 {
		t.Fatalf("без изменений версия не растёт: %+v %v", again, err)
	}
	if p, _ := store.Participant("t1"); p.Version != 2 {
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
	p, _ := store.Participant("t1")
	if participant.Restore(p).DeliveryValid() {
		t.Fatal("некорректные обязательные настройки должны скрыть участника")
	}
}
