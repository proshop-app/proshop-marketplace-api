package searchapi

import (
	"encoding/json"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func TestIdentityDocumentRoundTrip(t *testing.T) {
	source := ports.OfferDocument{
		TileKey:        "card",
		OnlyVariant:    true,
		ModelKey:       "s1:brand:model",
		ModelKeySource: "identity",
		VariantKey:     "exact:9780306406157",
	}
	document, err := toDocument(source)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var wire offerDocument
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	restored, err := fromDocument(wire)
	if restored.TileKey != source.TileKey || !restored.OnlyVariant {
		t.Fatal("ключ карточки или флаг варианта потерян при передаче", restored)
	}
	if err != nil || restored.ModelKey != source.ModelKey || restored.VariantKey != source.VariantKey ||
		restored.ModelKeySource != source.ModelKeySource {
		t.Fatal(restored, err)
	}
}
