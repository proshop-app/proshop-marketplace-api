package postgres

import (
	"slices"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
)

func TestCardAssetsRoundTrip(t *testing.T) {
	want := []string{"main", "detail"}
	card := catalog.Card{Assets: want}
	for i := 0; i < 2; i++ {
		raw, err := encodeCard(card)
		if err != nil {
			t.Fatal(err)
		}
		card, err = decodeCard(raw)
		if err != nil || !slices.Equal(card.Assets, want) {
			t.Fatal(card, err)
		}
	}
	old, err := decodeCard([]byte(`{"name":"Товар","visible":true}`))
	if err != nil || len(old.Assets) != 0 {
		t.Fatal(old, err)
	}
}
