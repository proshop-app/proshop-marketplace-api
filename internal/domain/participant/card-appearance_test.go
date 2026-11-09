package participant_test

import (
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func TestCardAppearance(t *testing.T) {
	for _, appearance := range []participant.CardAppearance{
		{},
		{Background: "#125f67", LogoBackground: "#e9eee7", TextColor: "white"},
	} {
		actual, err := appearance.Normalize()
		if err != nil || actual != participant.DefaultCardAppearance() {
			t.Fatal(actual, err)
		}
	}
	valid := participant.CardAppearance{Background: "#FFFFFF", LogoBackground: "#FFFFFF", TextColor: "black"}
	if actual, err := valid.Normalize(); err != nil || actual != valid {
		t.Fatal(actual, err)
	}
	// Зона логотипа не ограничивает цвет текста основной части.
	valid.LogoBackground = "#000000"
	if _, err := valid.Normalize(); err != nil {
		t.Fatal(err)
	}
}

func TestCardAppearanceRejectsUnreadableOrIncomplete(t *testing.T) {
	for _, appearance := range []participant.CardAppearance{
		{Background: "#FFFFFF", LogoBackground: "#FFFFFF", TextColor: "white"},
		{Background: "#000000", LogoBackground: "#FFFFFF", TextColor: "black"},
		{Background: "#125F67", LogoBackground: "#E9EEE7", TextColor: "red"},
		{Background: "#125F67", TextColor: "white"},
		{Background: "#123", LogoBackground: "#E9EEE7", TextColor: "white"},
	} {
		if _, err := appearance.Normalize(); !errors.Is(err, participant.ErrInvalidCardAppearance) {
			t.Fatal(appearance, err)
		}
		p := newParticipant(t)
		_, err := p.ApplyStoreCard(participant.Profile{Appearance: appearance}, participant.CardReadiness{}, nil, t0)
		if !errors.Is(err, participant.ErrInvalidCardAppearance) {
			t.Fatal(err)
		}
	}
}
