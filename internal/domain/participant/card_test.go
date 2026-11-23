package participant_test

import (
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func TestCardReadinessAndModeration(t *testing.T) {
	p := newParticipant(t)
	_, err := p.ApplyShopSettings(w3ParticipantDelivery, t0)
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
	profile := participant.Profile{Name: "Имя", Logo: &logo}
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
	if _, err := p.HideCard("logo", "Причина", t0); err != nil {
		t.Fatal(err)
	}
	if p.PublicProfile().Logo != nil {
		t.Fatal("скрытое раскрыто")
	}
	if changed, err := p.ApplyStoreCard(profile, ready, nil, t1); err != nil || changed {
		t.Fatal(changed, err)
	}
	if _, err := p.ApplyStoreCard(profile, participant.CardReadiness{}, nil, t1); err != nil {
		t.Fatal(err)
	}
	if p.PublicProfile().Logo != nil {
		t.Fatal(p.PublicProfile())
	}
	if !participant.VisibilityOf(p, activeTenant(t, participant.TenantActive)).Visible {
		t.Fatal("потеря готовности скрыла подключённый магазин")
	}
	if _, err := p.ApplyShopSettings(
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

func TestCardMarkFollowsLogoModeration(t *testing.T) {
	p := newParticipant(t)
	logo, mark := "logo", "logo-favicon|1@icon-192.png"
	profile := participant.Profile{Name: "Имя", Logo: &logo, Mark: &mark}
	ready := participant.CardReadiness{Ready: true}
	if _, err := p.ApplyStoreCard(profile, ready, nil, t0); err != nil {
		t.Fatal(err)
	}
	if public := p.PublicProfile(); public.Mark == nil || *public.Mark != mark {
		t.Fatal(public)
	}
	if _, err := p.HideCard(participant.CardLogo, "Причина", t0); err != nil {
		t.Fatal(err)
	}
	if public := p.PublicProfile(); public.Logo != nil || public.Mark != nil {
		t.Fatal("скрытие логотипа оставило знак", public)
	}
	snapshot := p.State()
	*snapshot.StoreCard.Profile.Mark = "Другой"
	if *p.State().StoreCard.Profile.Mark != mark {
		t.Fatal("снимок изменил знак агрегата")
	}
	next := "logo-favicon|2@icon-192.png"
	profile.Mark = &next
	if _, err := p.ApplyStoreCard(profile, ready, nil, t1); err != nil {
		t.Fatal(err)
	}
	if len(p.State().StoreCard.Hidden) != 0 {
		t.Fatal("смена знака не сняла скрытие")
	}
}

func TestCardModerationRejectsUnknownKeys(t *testing.T) {
	p := newParticipant(t)
	for _, key := range []string{"", "name", "unknown"} {
		if _, err := p.HideCard(key, "Причина", t0); !errors.Is(err, participant.ErrInvalidCardKey) {
			t.Fatal(key, err)
		}
		if _, err := p.ShowCard(key, t0); !errors.Is(err, participant.ErrInvalidCardKey) {
			t.Fatal(key, err)
		}
	}
}
