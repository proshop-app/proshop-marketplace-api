package participant_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func registryEvent(id string, slug string, status participant.TenantStatus, at time.Time) appparticipant.RegistryEvent {
	return appparticipant.RegistryEvent{EventID: id, Event: participant.RegistryEvent{
		TenantID: "t9",
		Slug:     slug,
		Status:   status,
		Cell:     "cell-a",
		Plan:     json.RawMessage(`{"code":"base"}`),
		Hosts:    json.RawMessage(`[]`),
		At:       at,
	}}
}

func TestRegistryMirrorsTenants(t *testing.T) {
	store := fake.NewStore()
	clock := fake.NewClock(t0)
	registry := appparticipant.NewRegistry(store, clock)
	ctx := context.Background()

	if err := registry.Apply(ctx, registryEvent("e1", "shop-nine", participant.TenantCreating, t0)); err != nil {
		t.Fatal(err)
	}
	event := registryEvent("e2", "shop-nine", participant.TenantActive, t0.Add(time.Minute))
	if err := registry.Apply(ctx, event); err != nil {
		t.Fatal(err)
	}

	tenant, ok := store.Tenant("t9")
	if !ok || tenant.Status != participant.TenantActive || tenant.Version != 2 {
		t.Fatalf("зеркало: %+v", tenant)
	}
	if len(store.OutboxRows()) != 0 {
		t.Fatal("магазин без участника не ставит задач индекса")
	}

	if err := registry.Apply(ctx, registryEvent("e3", "shop-nine", participant.TenantSuspended, t0)); err != nil {
		t.Fatal(err)
	}
	if tenant, _ := store.Tenant("t9"); tenant.Status != participant.TenantActive {
		t.Fatal("событие старше записи не применяется")
	}
}

func TestRegistryDeduplicatesEvents(t *testing.T) {
	store := fake.NewStore()
	registry := appparticipant.NewRegistry(store, fake.NewClock(t0))
	ctx := context.Background()

	if err := registry.Apply(ctx, registryEvent("e1", "shop-nine", participant.TenantActive, t0)); err != nil {
		t.Fatal(err)
	}
	event := registryEvent("e1", "other-slug", participant.TenantSuspended, t0.Add(time.Hour))
	if err := registry.Apply(ctx, event); err != nil {
		t.Fatal(err)
	}

	if tenant, _ := store.Tenant("t9"); tenant.Status != participant.TenantActive || tenant.Slug != "shop-nine" {
		t.Fatalf("повтор события с тем же идентификатором пропускается: %+v", tenant)
	}
}

func TestRegistrySyncsParticipant(t *testing.T) {
	store := fake.NewStore()
	clock := fake.NewClock(t0)
	registry := appparticipant.NewRegistry(store, clock)
	service := appparticipant.NewService(store, clock, &fake.IDs{})
	ctx := context.Background()

	if err := registry.Apply(ctx, registryEvent("e1", "shop-nine", participant.TenantActive, t0)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t9", Name: "Девятый"}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)

	renamed := registryEvent("e2", "shop-nine-renamed", participant.TenantSuspended, t0.Add(time.Minute))
	if err := registry.Apply(ctx, renamed); err != nil {
		t.Fatal(err)
	}

	p, _ := store.Participant("t9")
	if p.Slug != "shop-nine-renamed" || p.Version != 2 {
		t.Fatalf("слаг участника сверяется с реестром: %+v", p)
	}

	tasks := 0
	for _, row := range store.OutboxRows() {
		if row.Kind == appcatalog.TaskSyncTenant && row.TenantID == "t9" {
			tasks++
		}
	}
	if tasks != 1 {
		t.Fatalf("задача синхронизации склеивается до разбора, получено %d", tasks)
	}
}

func TestRegistryRejectsInvalidEvent(t *testing.T) {
	store := fake.NewStore()
	registry := appparticipant.NewRegistry(store, fake.NewClock(t0))

	err := registry.Apply(context.Background(), registryEvent("e1", "", participant.TenantActive, t0))
	if !errors.Is(err, participant.ErrInvalidRegistryEvent) {
		t.Fatalf("ожидалось ErrInvalidRegistryEvent, получено %v", err)
	}
	if store.Processed("e1") {
		t.Fatal("отвергнутое событие не отмечается обработанным: транзакция откатывается")
	}
}
