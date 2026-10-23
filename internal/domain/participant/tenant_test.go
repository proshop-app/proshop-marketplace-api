package participant_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func event(status participant.TenantStatus, at time.Time) participant.RegistryEvent {
	return participant.RegistryEvent{
		TenantID: "t1",
		Slug:     "shop-one",
		Status:   status,
		Cell:     "cell-a",
		Plan:     json.RawMessage(`{"code":"base"}`),
		Hosts:    json.RawMessage(`[]`),
		At:       at,
	}
}

func TestTenantAppliesNewerEventsOnly(t *testing.T) {
	tenant, err := participant.NewTenant(event(participant.TenantCreating, t0), t0)
	if err != nil {
		t.Fatal(err)
	}
	tenant.MarkSaved()

	changed, err := tenant.Apply(event(participant.TenantActive, t1), t1)
	if err != nil || !changed || tenant.Status() != participant.TenantActive || tenant.Version() != 2 {
		t.Fatalf("changed=%v err=%v status=%s version=%d", changed, err, tenant.Status(), tenant.Version())
	}
	tenant.MarkSaved()

	if changed, _ := tenant.Apply(event(participant.TenantSuspended, t0), t1); changed {
		t.Fatal("событие старше записи не применяется")
	}
	if changed, _ := tenant.Apply(event(participant.TenantActive, t1), t1); changed {
		t.Fatal("повтор того же снимка не меняет запись")
	}
	if changed, _ := tenant.Apply(event(participant.TenantSuspended, t1), t1); !changed {
		t.Fatal("другой снимок с тем же временем применяется")
	}
}

func TestTenantComparesJSONByMeaning(t *testing.T) {
	tenant, err := participant.NewTenant(event(participant.TenantActive, t0), t0)
	if err != nil {
		t.Fatal(err)
	}
	tenant.MarkSaved()

	reformatted := event(participant.TenantActive, t0)
	reformatted.Plan = json.RawMessage(`{ "code" : "base" }`)
	if changed, _ := tenant.Apply(reformatted, t1); changed {
		t.Fatal("переформатированный JSON — тот же снимок")
	}

	other := event(participant.TenantActive, t0)
	other.Plan = json.RawMessage(`{"code":"pro"}`)
	if changed, _ := tenant.Apply(other, t1); !changed {
		t.Fatal("другой тариф — изменение")
	}
}

func TestTenantDeletedIsTerminal(t *testing.T) {
	tenant, err := participant.NewTenant(event(participant.TenantDeleted, t0), t0)
	if err != nil {
		t.Fatal(err)
	}

	if changed, _ := tenant.Apply(event(participant.TenantActive, t1), t1); changed {
		t.Fatal("после удаления события не применяются")
	}
	if tenant.Joinable() {
		t.Fatal("удалённый магазин нельзя сделать участником")
	}
}

func TestTenantRejectsInvalidEvents(t *testing.T) {
	cases := map[string]participant.RegistryEvent{
		"пустой слаг":        {TenantID: "t1", Slug: " ", Status: participant.TenantActive, At: t0},
		"слаг с пробелом":    {TenantID: "t1", Slug: "a b", Status: participant.TenantActive, At: t0},
		"нет времени":        {TenantID: "t1", Slug: "a", Status: participant.TenantActive},
		"неизвестный статус": {TenantID: "t1", Slug: "a", Status: "frozen", At: t0},
	}

	for name, ev := range cases {
		if _, err := participant.NewTenant(ev, t0); !errors.Is(err, participant.ErrInvalidRegistryEvent) {
			t.Fatalf("%s: ожидалось ErrInvalidRegistryEvent, получено %v", name, err)
		}
	}

	tenant, err := participant.NewTenant(event(participant.TenantActive, t0), t0)
	if err != nil {
		t.Fatal(err)
	}

	other := event(participant.TenantActive, t1)
	other.TenantID = "t2"
	if _, err := tenant.Apply(other, t1); !errors.Is(err, participant.ErrInvalidRegistryEvent) {
		t.Fatalf("событие другого магазина: ожидалась ошибка, получено %v", err)
	}
}

func TestParseTenantStatus(t *testing.T) {
	for _, status := range []string{"creating", "active", "suspended", "deleting", "deleted"} {
		if _, err := participant.ParseTenantStatus(status); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
	}
	if _, err := participant.ParseTenantStatus("ACTIVE"); err == nil {
		t.Fatal("статус чувствителен к регистру")
	}
}
