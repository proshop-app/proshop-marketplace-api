package participant_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

var t0 = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*fake.Store, *fake.Clock, *appparticipant.Service) {
	t.Helper()

	store := fake.NewStore()
	clock := fake.NewClock(t0)
	store.SeedTenant(participant.TenantState{
		TenantID: "t1", Slug: "shop-one", Status: participant.TenantActive, EventAt: t0, Version: 1, UpdatedAt: t0,
	})

	return store, clock, appparticipant.NewService(store, clock)
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
	if len(rows) != 1 || rows[0].Kind != appcatalog.TaskSyncTenant || rows[0].TenantID != "t1" {
		t.Fatalf("ожидалась задача синхронизации магазина: %+v", rows)
	}

	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: "Ещё раз"}); !errors.Is(err, participant.ErrAlreadyExists) {
		t.Fatalf("повтор: ожидалось ErrAlreadyExists, получено %v", err)
	}
}

func TestCreateRequiresRegistry(t *testing.T) {
	store, _, service := setup(t)
	ctx := context.Background()

	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "unknown", Name: "x"}); !errors.Is(err, participant.ErrTenantUnknown) {
		t.Fatalf("ожидалось ErrTenantUnknown, получено %v", err)
	}

	store.SeedTenant(participant.TenantState{TenantID: "t2", Slug: "gone", Status: participant.TenantDeleting, EventAt: t0, Version: 1})
	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t2", Name: "x"}); !errors.Is(err, participant.ErrTenantUnavailable) {
		t.Fatalf("ожидалось ErrTenantUnavailable, получено %v", err)
	}

	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: ""}); !errors.Is(err, participant.ErrInvalidName) {
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

	view, err := service.Enable(ctx, "t1")
	if err != nil || !view.Visibility.Visible || view.Participant.Version != 2 {
		t.Fatalf("включение: %+v, %v", view, err)
	}

	view, err = service.SetPaymentAccount(ctx, "t1", "acc-1")
	if err != nil || !view.Visibility.CheckoutAvailable || view.Participant.Status != participant.StatusConnected {
		t.Fatalf("оплата: %+v, %v", view, err)
	}

	if _, err := service.Rename(ctx, "t1", "Новое имя"); err != nil {
		t.Fatal(err)
	}

	view, err = service.Disable(ctx, "t1")
	if err != nil || view.Visibility.Visible || view.Participant.Version != 5 {
		t.Fatalf("выключение: %+v, %v", view, err)
	}

	before := len(store.OutboxRows())
	if _, err := service.Disable(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if len(store.OutboxRows()) != before {
		t.Fatal("повтор без изменения не ставит задач")
	}
}

func TestChangeErrors(t *testing.T) {
	_, _, service := setup(t)
	ctx := context.Background()

	if _, err := service.Enable(ctx, "missing"); !errors.Is(err, participant.ErrNotFound) {
		t.Fatalf("ожидалось ErrNotFound, получено %v", err)
	}
	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: "Магазин"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetPaymentAccount(ctx, "t1", "плохой"); !errors.Is(err, participant.ErrInvalidPaymentAccount) {
		t.Fatalf("ожидалось ErrInvalidPaymentAccount, получено %v", err)
	}
}

func TestGetAndList(t *testing.T) {
	store, clock, service := setup(t)
	ctx := context.Background()

	store.SeedTenant(participant.TenantState{TenantID: "t2", Slug: "shop-two", Status: participant.TenantSuspended, EventAt: t0, Version: 1})

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

	if _, err := service.Create(context.Background(), appparticipant.CreateCommand{TenantID: "t1", Name: "Магазин"}); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if _, ok := store.Participant("t1"); ok || len(store.OutboxRows()) != 0 {
		t.Fatal("несостоявшаяся транзакция не оставляет ни участника, ни задачи")
	}
}
