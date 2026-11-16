package taxonomydata

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/colorpalette"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

func TestColorQualityIncludesOnlyUnmappedConfirmedEntries(t *testing.T) {
	var output bytes.Buffer
	entries := []*reference.Entry{
		reference.RestoreEntry(reference.EntryState{
			CanonicalName: "Старый оттенок",
			Status:        reference.StatusConfirmed,
		}),
		reference.RestoreEntry(reference.EntryState{
			Dictionary:    "color",
			CanonicalName: "Оттенок",
			Status:        reference.StatusCandidate,
		}),
		reference.RestoreEntry(reference.EntryState{
			Dictionary:    "color",
			CanonicalName: "Хаки",
			Status:        reference.StatusConfirmed,
			Metadata:      json.RawMessage(`{"baseColors":["Зелёный"]}`),
		}),
		reference.RestoreEntry(reference.EntryState{
			Dictionary:    "color",
			CanonicalName: "Зелёный",
			Status:        reference.StatusConfirmed,
		}),
	}
	if err := WriteQuality(&output, entries); err != nil {
		t.Fatal(err)
	}
	if output.String() != "Цвета без базового\nСтарый оттенок\n" {
		t.Fatalf("неверный отчёт: %s", output.String())
	}
}

func TestPaletteSamplesAreComplete(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range release.Attributes {
		if field.Dictionary != colorpalette.Dictionary {
			continue
		}
		if !field.FacetPinned {
			t.Fatal("цвет должен закрепляться данными поставки")
		}
		palette := colorpalette.Values()
		if len(palette) != 18 || len(field.ValuesMeta) != len(palette) {
			t.Fatal("палитра расходится с набором образцов")
		}
		for _, value := range palette {
			sample := field.ValuesMeta[value]
			if sample.Hex == "" && sample.Swatch == "" {
				t.Errorf("нет образца базового цвета %s", value)
			}
		}
		return
	}
	t.Fatal("атрибут color отсутствует")
}
