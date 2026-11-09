package participant_test

import (
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func TestCardAppearance(t *testing.T) {
	for _, appearance := range []participant.CardAppearance{
		{},
		{Color: "#125f67"},
	} {
		actual, err := appearance.Normalize()
		if err != nil || actual != participant.DefaultCardAppearance() {
			t.Fatal(actual, err)
		}
	}
	// Светлый и насыщенный цвет допустимы: читаемые оттенки вычисляет витрина.
	for _, color := range []string{"#FFFFFF", "#000000", "#FF0000", "#FFFF00"} {
		appearance := participant.CardAppearance{Color: color}
		if actual, err := appearance.Normalize(); err != nil || actual != appearance {
			t.Fatal(actual, err)
		}
	}
}

func TestCardAppearanceRejectsIncompleteHex(t *testing.T) {
	for _, color := range []string{"#123", "#12345678", "red", "#12FG67", " "} {
		appearance := participant.CardAppearance{Color: color}
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
