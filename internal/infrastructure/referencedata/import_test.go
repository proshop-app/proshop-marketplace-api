package referencedata

import (
	"encoding/json"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

func TestLoadImportPackage(t *testing.T) {
	input := reference.ImportPackage{
		SchemaVersion: 1,
		Sources:       []reference.ImportSource{{ID: "s1", URL: "https://example.org/data", License: "CC0-1.0"}},
		Dictionaries: []reference.ImportDictionary{
			{Code: "d1", Kind: reference.KindOpen, Release: 1, Entries: []reference.DeliveryEntry{}},
			{
				Code:    "d2",
				Kind:    reference.KindClosed,
				Release: 1,
				Entries: []reference.DeliveryEntry{{Canonical: "Значение", Status: reference.StatusConfirmed}},
			},
		},
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	source := fstest.MapFS{"package.json": &fstest.MapFile{Data: body}}
	loaded, err := LoadImport(source, "package.json")
	if err != nil || len(loaded.Dictionaries) != 2 || loaded.Dictionaries[0].Entries == nil {
		t.Fatal(loaded, err)
	}
	for _, body := range []string{
		"{",
		"null",
		`{"schemaVersion": 2}`,
		`{"schemaVersion": 1, "unexpected": true}`,
		string(body) + " {}",
	} {
		source["package.json"].Data = []byte(body)
		if _, err := LoadImport(source, "package.json"); !errors.Is(err, reference.ErrInvalidDelivery) {
			t.Fatalf("некорректный пакет принят: %v", err)
		}
	}
	if _, err := LoadImport(source, "missing.json"); !errors.Is(err, reference.ErrInvalidDelivery) {
		t.Fatal(err)
	}
}
