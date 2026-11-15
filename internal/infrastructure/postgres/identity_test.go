package postgres

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestCardIdentityRoundTrip(t *testing.T) {
	source := catalog.Card{
		ModelKey:       "s1:brand:model",
		ModelKeySource: "identity",
		VariantKey:     "s1:brand:model:axis",
		Publication:    taxonomy.Publication{Schema: "s1"},
	}
	raw, err := encodeCard(source)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := decodeCard(raw)
	if err != nil || restored.ModelKey != source.ModelKey || restored.VariantKey != source.VariantKey ||
		restored.ModelKeySource != source.ModelKeySource || restored.Publication.Schema != source.Publication.Schema {
		t.Fatal(restored, err)
	}
	old, err := decodeCard([]byte(`{"name":"Товар"}`))
	if err != nil || old.VariantKey != "" {
		t.Fatal(old, err)
	}
}
