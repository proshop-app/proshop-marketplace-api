package participant_test

import (
	"context"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

// settingsTasks — невзятые задачи сверки настроек.
func settingsTasks(store *fake.Store) []fake.OutboxRow {
	var tasks []fake.OutboxRow

	for _, row := range store.OutboxRows() {
		if row.Kind == appparticipant.TaskSyncSettings && row.DoneAt == nil {
			tasks = append(tasks, row)
		}
	}

	return tasks
}

func TestResyncOnMarketplaceDisabled(t *testing.T) {
	store, clock, _ := setup(t)
	ctx := context.Background()
	platform := fake.NewPlatform()
	resyncing := appparticipant.NewResyncingPlatform(platform, store, clock)

	platform.Fail("t1", ports.ErrMarketplaceDisabled)

	_, err := resyncing.CheckItems(ctx, "t1", []ports.ItemRequest{{ProductID: "p1", Quantity: 1}})
	if !errors.Is(err, ports.ErrMarketplaceDisabled) {
		t.Fatalf("отказ сервера возвращается как есть: %v", err)
	}

	_, err = resyncing.QuoteDelivery(ctx, "t1", ports.QuoteRequest{Carrier: "cdek"})
	if !errors.Is(err, ports.ErrMarketplaceDisabled) {
		t.Fatalf("отказ сервера на расчёте: %v", err)
	}

	tasks := settingsTasks(store)
	if len(tasks) != 1 || tasks[0].TenantID != "t1" || tasks[0].DedupKey != "participant.settings.sync:t1" {
		t.Fatalf("одна внеочередная сверка на магазин, повтор склеивается: %+v", tasks)
	}

	platform.Fail("t2", errors.New("сервер недоступен"))
	if _, err := resyncing.CheckItems(ctx, "t2", nil); err == nil || len(settingsTasks(store)) != 1 {
		t.Fatalf("прочие сбои сверку не ставят: %v", err)
	}
}

func TestSettingsTaskDisablesShop(t *testing.T) {
	store, clock, service := setup(t)
	ctx := context.Background()

	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: "Магазин"}); err != nil {
		t.Fatal(err)
	}

	platform := fake.NewPlatform()
	settings := appparticipant.NewSettings(store, platform, clock)

	platform.SetSettings("t1", ports.ShopSettings{
		Enabled:  true,
		Subsidy:  participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed},
		Delivery: participant.DeliverySettings{Regions: []string{"cdek:r1"}, Carriers: []string{"cdek"}},
	})
	if changed, err := settings.SyncOne(ctx, "t1"); err != nil || !changed {
		t.Fatalf("участие включено: %v %v", changed, err)
	}

	platform.Fail("t1", ports.ErrMarketplaceDisabled)

	task := appparticipant.SyncSettingsTask("t1", clock.Now())
	if err := settings.HandleTask(ctx, taskOf(task)); err != nil {
		t.Fatal(err)
	}
	if p, _ := store.Participant("t1"); p.ShopEnabled {
		t.Fatalf("отказ «магазин не участвует» выключает участие: %+v", p)
	}

	unknown := appparticipant.SyncSettingsTask("t9", clock.Now())
	if err := settings.HandleTask(ctx, taskOf(unknown)); err != nil {
		t.Fatalf("участника нет — сверять нечего: %v", err)
	}

	mismatch := ports.Task{Kind: task.Kind, TenantID: "t2", Payload: task.Payload}
	if err := settings.HandleTask(ctx, mismatch); !errors.Is(err, appparticipant.ErrInvalidTask) {
		t.Fatalf("магазин задачи не совпадает: %v", err)
	}

	broken := ports.Task{Kind: task.Kind, TenantID: "t1"}
	if err := settings.HandleTask(ctx, broken); !errors.Is(err, appparticipant.ErrInvalidTask) {
		t.Fatalf("повреждённая задача: %v", err)
	}

	if p, _ := store.Participant("t1"); p.Status != participant.StatusOnboarding {
		t.Fatalf("выключенное участие возвращает статус настройки: %+v", p)
	}
}

// taskOf — задача outbox в том виде, в каком её отдаёт очередь.
func taskOf(task ports.NewTask) ports.Task {
	return ports.Task{
		Kind:     task.Kind,
		TenantID: task.TenantID,
		Payload:  task.Payload,
	}
}
