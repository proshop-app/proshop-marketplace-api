package participant_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

var (
	t0 = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Minute)
)

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
	if p.Status() != participant.StatusOnboarding || p.Enabled() || p.PaymentAccountID() != "" {
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
	p.MarkSaved()

	return participant.Restore(p.State())
}

func TestSetPaymentAccountTransitions(t *testing.T) {
	p := restored(t)

	changed, err := p.SetPaymentAccount(" 506751 ", t1)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if p.Status() != participant.StatusConnected || p.PaymentAccountID() != "506751" {
		t.Fatalf("ожидался connected: %+v", p.State())
	}
	if p.Origin().Status != participant.StatusOnboarding || p.Origin().Version != 1 || p.Version() != 2 {
		t.Fatalf(
			"исходное состояние должно остаться для условного обновления: origin=%+v version=%d",
			p.Origin(),
			p.Version(),
		)
	}

	if changed, _ := p.SetPaymentAccount("506751", t1); changed {
		t.Fatal("повтор того же идентификатора не меняет состояние")
	}

	if _, err := p.SetPaymentAccount("bad id", t1); !errors.Is(err, participant.ErrInvalidPaymentAccount) {
		t.Fatalf("ожидалось ErrInvalidPaymentAccount, получено %v", err)
	}
}

func TestEnableDisableIdempotent(t *testing.T) {
	p := restored(t)

	if !p.Enable(t1) || p.Enable(t1) {
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

	p.Enable(t1)
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

	p.Enable(t1)
	if got := participant.VisibilityOf(p, active); got.Visible {
		t.Fatalf("включённый оператором, но не включившийся сам магазин не виден: %+v", got)
	}

	if _, err := p.ApplyShopSettings(true, participant.Subsidy{}, t1); err != nil {
		t.Fatal(err)
	}
	if got := participant.VisibilityOf(p, active); !got.Visible || got.CheckoutAvailable {
		t.Fatalf("включённый без оплаты виден, но без checkout: %+v", got)
	}

	if _, err := p.SetPaymentAccount("acc-1", t1); err != nil {
		t.Fatal(err)
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
	subsidy := participant.Subsidy{
		Enabled:   true,
		Type:      participant.SubsidyPercent,
		Value:     50,
		MaxAmount: money.FromKopecks(30000),
	}

	changed, err := p.ApplyShopSettings(true, subsidy, t1)
	if err != nil || !changed || !p.ShopEnabled() || p.Subsidy() != subsidy || p.Version() != 2 {
		t.Fatalf("настройки магазина: changed=%v err=%v %+v", changed, err, p.State())
	}
	p.MarkSaved()

	changed, err = p.ApplyShopSettings(true, subsidy, t1.Add(time.Hour))
	if err != nil || changed || p.Version() != 2 || !p.State().SettingsSyncedAt.Equal(t1.Add(time.Hour)) {
		t.Fatalf("те же настройки не меняют версию, но время сверки обновляется: %+v", p.State())
	}

	overPercent := participant.Subsidy{Enabled: true, Type: participant.SubsidyPercent, Value: 101}

	_, err = p.ApplyShopSettings(true, overPercent, t1)
	if !errors.Is(err, participant.ErrInvalidSubsidy) {
		t.Fatalf("процент больше 100: %v", err)
	}
}

func TestSubsidyApply(t *testing.T) {
	cost := money.FromKopecks(40000)
	cases := map[string]struct {
		subsidy participant.Subsidy
		want    int64
	}{
		"выключена": {participant.Subsidy{Type: participant.SubsidyFixed, Value: 10000}, 0},
		"фиксированная": {
			participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed, Value: 15000},
			15000,
		},
		"больше стоимости": {
			participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed, Value: 99999},
			40000,
		},
		"процент": {participant.Subsidy{Enabled: true, Type: participant.SubsidyPercent, Value: 25}, 10000},
		"процент под потолок": {
			participant.Subsidy{
				Enabled:   true,
				Type:      participant.SubsidyPercent,
				Value:     50,
				MaxAmount: money.FromKopecks(12000),
			},
			12000,
		},
	}

	for name, tc := range cases {
		got, err := tc.subsidy.Apply(cost)
		if err != nil || got.Kopecks() != tc.want {
			t.Fatalf("%s: %d, %v, ожидалось %d", name, got.Kopecks(), err, tc.want)
		}
	}

	fixed := participant.Subsidy{Enabled: true, Type: participant.SubsidyFixed, Value: 100}
	if got, _ := fixed.Apply(money.Money{}); !got.IsZero() {
		t.Fatal("бесплатная доставка — субсидии нет")
	}

	err := (participant.Subsidy{Enabled: true, Type: "gift"}).Validate()
	if !errors.Is(err, participant.ErrInvalidSubsidy) {
		t.Fatalf("неизвестный вид субсидии: %v", err)
	}
}
