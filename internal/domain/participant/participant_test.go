package participant_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

var (
	t0 = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Minute)
)

var w3ParticipantDelivery = participant.DeliverySettings{
	Regions:  []string{"RU-MOW"},
	Carriers: []string{"cdek", "yandex"},
}

func activeTenant(t *testing.T, status participant.TenantStatus) *participant.Tenant {
	t.Helper()

	event := participant.RegistryEvent{
		TenantID: "t1",
		Slug:     "shop-one",
		Status:   status,
		At:       t0,
	}

	tenant, err := participant.NewTenant(event, t0)
	if err != nil {
		t.Fatal(err)
	}

	return tenant
}

func newParticipant(t *testing.T) *participant.Participant {
	t.Helper()

	p, err := participant.New(activeTenant(t, participant.TenantActive), "  Магазин один  ", t0)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func TestNewParticipant(t *testing.T) {
	p := newParticipant(t)

	if p.TenantID() != "t1" || p.Slug() != "shop-one" || p.Name() != "Магазин один" {
		t.Fatalf("неожиданный участник: %+v", p.State())
	}
	if p.Stage() != participant.StageCandidate || p.Enabled() {
		t.Fatalf("новый участник должен быть скрыт и не подключён: %+v", p.State())
	}
	if !p.IsNew() || p.Version() != 1 {
		t.Fatalf("версия %d, новый %v", p.Version(), p.IsNew())
	}
}

func TestNewRejectsUnavailableTenantAndBadName(t *testing.T) {
	for _, status := range []participant.TenantStatus{participant.TenantDeleting, participant.TenantDeleted} {
		_, err := participant.New(activeTenant(t, status), "x", t0)
		if !errors.Is(err, participant.ErrTenantUnavailable) {
			t.Fatalf("%s: ожидалось ErrTenantUnavailable, получено %v", status, err)
		}
	}

	for _, status := range []participant.TenantStatus{participant.TenantCreating, participant.TenantSuspended} {
		if _, err := participant.New(activeTenant(t, status), "x", t0); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
	}

	for _, name := range []string{"", "   ", strings.Repeat("я", 201), "a\nb"} {
		_, err := participant.New(activeTenant(t, participant.TenantActive), name, t0)
		if !errors.Is(err, participant.ErrInvalidName) {
			t.Fatalf("%q: ожидалось ErrInvalidName, получено %v", name, err)
		}
	}
}

func restored(t *testing.T) *participant.Participant {
	t.Helper()

	p := newParticipant(t)
	if _, err := p.ApplyStoreCard(participant.Profile{}, participant.CardReadiness{Ready: true}, nil, t0); err != nil {
		t.Fatal(err)
	}
	p.MarkSaved()

	return participant.Restore(p.State())
}

func TestEnableDisableIdempotent(t *testing.T) {
	p := restored(t)

	changed, enableErr := p.Enable(t1)
	repeated, repeatErr := p.Enable(t1)
	if !changed || enableErr != nil || repeated || repeatErr != nil {
		t.Fatal("включение должно сработать один раз")
	}
	if !p.Enabled() || p.Version() != 2 {
		t.Fatalf("включён=%v версия=%d", p.Enabled(), p.Version())
	}

	p.MarkSaved()

	if !p.Disable(t1) || p.Disable(t1) {
		t.Fatal("выключение должно сработать один раз")
	}
	if p.Enabled() || p.Version() != 3 {
		t.Fatalf("включён=%v версия=%d", p.Enabled(), p.Version())
	}
}

func TestVersionGrowsOncePerSave(t *testing.T) {
	p := restored(t)

	if _, err := p.Enable(t1); err != nil {

		t.Fatal(err)

	}
	if _, err := p.Rename("Новое имя", t1); err != nil {
		t.Fatal(err)
	}

	if p.Version() != 2 {
		t.Fatalf("за одно сохранение версия растёт на единицу, получено %d", p.Version())
	}
}

func TestRename(t *testing.T) {
	p := restored(t)

	if changed, err := p.Rename("Магазин один", t1); err != nil || changed {
		t.Fatalf("то же имя: changed=%v err=%v", changed, err)
	}
	if _, err := p.Rename("", t1); !errors.Is(err, participant.ErrInvalidName) {
		t.Fatalf("ожидалось ErrInvalidName, получено %v", err)
	}
}

func TestSyncSlug(t *testing.T) {
	p := restored(t)
	tenant := activeTenant(t, participant.TenantActive)

	if p.SyncSlug(tenant, t1) {
		t.Fatal("тот же слаг не меняет участника")
	}

	renamed := participant.RegistryEvent{TenantID: "t1", Slug: "shop-renamed", Status: participant.TenantActive, At: t1}
	if _, err := tenant.Apply(renamed, t1); err != nil {
		t.Fatal(err)
	}
	if !p.SyncSlug(tenant, t1) || p.Slug() != "shop-renamed" {
		t.Fatalf("слаг %q", p.Slug())
	}
}

func TestVisibility(t *testing.T) {
	p := restored(t)
	active := activeTenant(t, participant.TenantActive)
	suspended := activeTenant(t, participant.TenantSuspended)

	if got := participant.VisibilityOf(p, active); got.Visible || got.CheckoutAvailable {
		t.Fatalf("выключенный участник не виден: %+v", got)
	}

	if _, err := p.Enable(t1); err != nil {

		t.Fatal(err)

	}
	if got := participant.VisibilityOf(p, active); got.Visible || got.CheckoutAvailable {
		t.Fatalf("без доставки магазин скрыт: %+v", got)
	}

	if _, err := p.ApplyShopSettings(w3ParticipantDelivery, t1); err != nil {
		t.Fatal(err)
	}
	if got := participant.VisibilityOf(p, active); !got.Visible || !got.CheckoutAvailable {
		t.Fatalf("участник с настройками доступен для оформления: %+v", got)
	}

	if got := participant.VisibilityOf(p, active); !got.Visible || !got.CheckoutAvailable {
		t.Fatalf("подключённый виден и доступен в checkout: %+v", got)
	}
	if got := participant.VisibilityOf(p, suspended); got.Visible || got.CheckoutAvailable {
		t.Fatalf("приостановленный магазин не виден: %+v", got)
	}
	if got := participant.VisibilityOf(nil, active); got.Visible {
		t.Fatal("без участника магазин не виден")
	}
}

func TestApplyShopSettings(t *testing.T) {
	p := restored(t)

	changed, err := p.ApplyShopSettings(w3ParticipantDelivery, t1)
	if err != nil || !changed || !p.DeliveryValid() || p.Version() != 2 {
		t.Fatalf("настройки магазина: changed=%v err=%v %+v", changed, err, p.State())
	}
	p.MarkSaved()

	changed, err = p.ApplyShopSettings(w3ParticipantDelivery, t1.Add(time.Hour))
	if err != nil || changed || p.Version() != 2 || !p.State().SettingsSyncedAt.Equal(t1.Add(time.Hour)) {
		t.Fatalf("те же настройки не меняют версию, но время сверки обновляется: %+v", p.State())
	}

}

func TestVisibilityRequiresMatchingActiveTenant(t *testing.T) {
	p := participant.Restore(participant.State{
		TenantID: "t1",
		Stage:    participant.StageConnected,
		Delivery: w3ParticipantDelivery,
	})
	if got := participant.VisibilityOf(p, nil); got.CheckoutAvailable {
		t.Fatal("нет записи реестра", got)
	}
	mismatched := participant.RestoreTenant(participant.TenantState{TenantID: "t2", Status: participant.TenantActive})
	if got := participant.VisibilityOf(p, mismatched); got.CheckoutAvailable {
		t.Fatal("чужая запись реестра", got)
	}
	for _, status := range []participant.TenantStatus{
		participant.TenantCreating,
		participant.TenantSuspended,
		participant.TenantDeleting,
		participant.TenantDeleted,
	} {
		tenant := activeTenant(t, status)
		if got := participant.VisibilityOf(p, tenant); got.CheckoutAvailable {
			t.Fatal(status, got)
		}
	}
	for _, stage := range []participant.Stage{
		participant.StageCandidate,
		participant.StageReturned,
		participant.StageDisconnected,
	} {
		state := p.State()
		state.Stage = stage
		got := participant.VisibilityOf(participant.Restore(state), activeTenant(t, participant.TenantActive))
		if got.CheckoutAvailable {
			t.Fatal(stage, got)
		}
	}
}
