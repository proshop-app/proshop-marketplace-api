package participant_test

import (
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestCardReadinessAndModeration(t *testing.T) {
	p := newParticipant(t)
	_, err := p.ApplyShopSettings(w3ParticipantSubsidy, money.Money{}, w3ParticipantDelivery, t0)
	if err != nil {
		t.Fatal(err)
	}
	if p.InQueue() {
		t.Fatal("неготовый кандидат попал в очередь")
	}
	if _, err := p.Enable(t0); !errors.Is(err, participant.ErrCardNotReady) {
		t.Fatal(err)
	}
	logo := "logo"
	profile := participant.Profile{Name: "Имя", Slogan: "Слоган", Logo: &logo}
	ready := participant.CardReadiness{Ready: true}
	if _, err := p.ApplyStoreCard(profile, ready, nil, t0); err != nil {
		t.Fatal(err)
	}
	if !p.InQueue() {
		t.Fatal("готовый кандидат не попал в очередь")
	}
	if _, err := p.Enable(t0); err != nil {
		t.Fatal(err)
	}
	if _, err := p.HideCard("slogan", "Причина", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := p.HideCard("logo", "Причина", t0); err != nil {
		t.Fatal(err)
	}
	if p.PublicProfile().Slogan != "" || p.PublicProfile().Logo != nil {
		t.Fatal("скрытое раскрыто")
	}
	if changed, err := p.ApplyStoreCard(profile, ready, nil, t1); err != nil || changed {
		t.Fatal(changed, err)
	}
	profile.Slogan = "Исправлено"
	if _, err := p.ApplyStoreCard(profile, participant.CardReadiness{}, nil, t1); err != nil {
		t.Fatal(err)
	}
	if p.PublicProfile().Slogan != "Исправлено" || p.PublicProfile().Logo != nil {
		t.Fatal(p.PublicProfile())
	}
	if !participant.VisibilityOf(p, activeTenant(t, participant.TenantActive)).Visible {
		t.Fatal("потеря готовности скрыла подключённый магазин")
	}
	if _, err := p.ApplyShopSettings(
		participant.Subsidy{},
		money.Money{},
		participant.DeliverySettings{},
		t1,
	); err != nil {
		t.Fatal(err)
	}
	visibility := participant.VisibilityOf(p, activeTenant(t, participant.TenantActive))
	if visibility.Visible || visibility.CheckoutAvailable {
		t.Fatal(visibility)
	}
	snapshot := p.State()
	snapshot.StoreCard.Hidden[0].Reason = "Другая"
	*snapshot.StoreCard.Profile.Logo = "Другой"
	if p.State().StoreCard.Hidden[0].Reason != "Причина" || *p.State().StoreCard.Profile.Logo != "logo" {
		t.Fatal("снимок изменил агрегат")
	}
	profile.Logo = nil
	if _, err := p.ApplyStoreCard(profile, ready, nil, t1); err != nil {
		t.Fatal(err)
	}
	if len(p.State().StoreCard.Hidden) != 0 {
		t.Fatal("смена логотипа не сняла скрытие")
	}
}
