package participant_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

var t0 = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*fake.Store, *fake.Clock, *appparticipant.Service) {
	t.Helper()

	store := fake.NewStore()
	clock := fake.NewClock(t0)
	store.SeedTenant(participant.TenantState{
		TenantID:  "t1",
		Slug:      "shop-one",
		Status:    participant.TenantActive,
		EventAt:   t0,
		Version:   1,
		UpdatedAt: t0,
	})

	return store, clock, appparticipant.NewService(store, clock, &fake.IDs{})
}

func TestCreate(t *testing.T) {
	store, _, service := setup(t)
	ctx := context.Background()

	view, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: "Магазин"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Participant.Slug != "shop-one" || view.TenantStatus != participant.TenantActive || view.Visibility.Visible {
		t.Fatalf("неожиданный участник: %+v", view)
	}

	rows := store.OutboxRows()
	if len(rows) != 2 || rows[0].Kind != appcatalog.TaskSyncTenant || rows[0].TenantID != "t1" ||
		rows[1].Kind != appmoderation.TaskReevaluate || rows[1].TenantID != "t1" {
		t.Fatalf("ожидались задачи синхронизации магазина и переоценки модерации: %+v", rows)
	}

	_, err = service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: "Ещё раз"})
	if !errors.Is(err, participant.ErrAlreadyExists) {
		t.Fatalf("повтор: ожидалось ErrAlreadyExists, получено %v", err)
	}
}

func TestCreateRequiresRegistry(t *testing.T) {
	store, _, service := setup(t)
	ctx := context.Background()

	_, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "unknown", Name: "x"})
	if !errors.Is(err, participant.ErrTenantUnknown) {
		t.Fatalf("ожидалось ErrTenantUnknown, получено %v", err)
	}

	store.SeedTenant(participant.TenantState{
		TenantID: "t2",
		Slug:     "gone",
		Status:   participant.TenantDeleting,
		EventAt:  t0,
		Version:  1,
	})
	_, err = service.Create(ctx, appparticipant.CreateCommand{TenantID: "t2", Name: "x"})
	if !errors.Is(err, participant.ErrTenantUnavailable) {
		t.Fatalf("ожидалось ErrTenantUnavailable, получено %v", err)
	}

	_, err = service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: ""})
	if !errors.Is(err, participant.ErrInvalidName) {
		t.Fatalf("ожидалось ErrInvalidName, получено %v", err)
	}
	if len(store.OutboxRows()) != 0 {
		t.Fatal("отказ не оставляет задач")
	}
}

func TestChangesEnqueueSyncOnlyWhenChanged(t *testing.T) {
	store, clock, service := setup(t)
	ctx := context.Background()

	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: "Магазин"}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)

	view, err := service.Enable(ctx, "t1", participant.Decision{Note: "Заметка"})
	if err != nil || !view.Participant.Enabled || view.Visibility.Visible || view.Participant.Version != 2 {
		t.Fatalf("включение оператором без настроек доставки не делает видимым: %+v, %v", view, err)
	}

	if _, err := service.Rename(ctx, "t1", "Новое имя"); err != nil {
		t.Fatal(err)
	}

	view, err = service.Disable(ctx, "t1", participant.Decision{Note: "Заметка"})
	if err != nil || view.Visibility.Visible || view.Participant.Version != 4 {
		t.Fatalf("выключение: %+v, %v", view, err)
	}

	before := len(store.OutboxRows())
	if _, err := service.Disable(ctx, "t1", participant.Decision{Note: "Заметка"}); err != nil {
		t.Fatal(err)
	}
	if len(store.OutboxRows()) != before {
		t.Fatal("повтор без изменения не ставит задач")
	}
}

func TestChangeErrors(t *testing.T) {
	_, _, service := setup(t)
	ctx := context.Background()

	if _, err := service.Enable(ctx, "missing", participant.Decision{}); !errors.Is(err, participant.ErrNotFound) {
		t.Fatalf("ожидалось ErrNotFound, получено %v", err)
	}
	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: "Магазин"}); err != nil {
		t.Fatal(err)
	}

}

func TestGetAndList(t *testing.T) {
	store, clock, service := setup(t)
	ctx := context.Background()

	store.SeedTenant(participant.TenantState{
		TenantID: "t2",
		Slug:     "shop-two",
		Status:   participant.TenantSuspended,
		EventAt:  t0,
		Version:  1,
	})

	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: "Первый"}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t2", Name: "Второй"}); err != nil {
		t.Fatal(err)
	}

	view, err := service.Get(ctx, "t2")
	if err != nil || view.TenantStatus != participant.TenantSuspended {
		t.Fatalf("%+v, %v", view, err)
	}

	list, err := service.List(ctx, participant.Page{Limit: 1, Offset: 1})
	if err != nil || list.Total != 2 || len(list.Items) != 1 || list.Items[0].Participant.TenantID != "t2" {
		t.Fatalf("%+v, %v", list, err)
	}

	list, err = service.List(ctx, participant.Page{Limit: 0, Offset: -5})
	if err != nil || len(list.Items) != 2 {
		t.Fatalf("лимит и смещение по умолчанию: %+v, %v", list, err)
	}

	if _, err := service.Get(ctx, "missing"); !errors.Is(err, participant.ErrNotFound) {
		t.Fatalf("ожидалось ErrNotFound, получено %v", err)
	}
}

func TestFailedCommitLeavesNothing(t *testing.T) {
	store, _, service := setup(t)
	store.FailCommit = errors.New("сбой фиксации")

	command := appparticipant.CreateCommand{TenantID: "t1", Name: "Магазин"}
	if _, err := service.Create(context.Background(), command); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if _, ok := store.Participant("t1"); ok || len(store.OutboxRows()) != 0 {
		t.Fatal("несостоявшаяся транзакция не оставляет ни участника, ни задачи")
	}
}

func participantID(value string) ids.TenantID { return ids.TenantID(value) }

func TestDeliveryVersionAndTransactionalRollback(t *testing.T) {
	store, _, service := setup(t)
	ctx := context.Background()
	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: "Магазин"}); err != nil {
		t.Fatal(err)
	}
	settings := participant.DeliverySettings{Regions: []string{"RU-MOW"}, Carriers: []string{"cdek"}}
	store.FailCommit = errors.New("commit")
	if _, err := service.SetDelivery(ctx, "t1", settings, 1); err == nil {
		t.Fatal("ожидался откат")
	}
	store.FailCommit = nil
	view, err := service.Get(ctx, "t1")
	if err != nil || view.Participant.Version != 1 || len(view.Participant.Delivery.Regions) != 0 {
		t.Fatal(view, err)
	}
	view, err = service.SetDelivery(ctx, "t1", settings, 1)
	if err != nil || view.Participant.Version != 2 {
		t.Fatal(view, err)
	}
	_, err = service.SetDelivery(ctx, "t1", settings, 1)
	if !errors.Is(err, participant.ErrConflict) {
		t.Fatal(err)
	}
	_, err = service.SetDelivery(ctx, "t1", participant.DeliverySettings{}, 2)
	if !errors.Is(err, participant.ErrInvalidDeliverySettings) {
		t.Fatal(err)
	}
}
