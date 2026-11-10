package reference

import (
	"encoding/json"
	"testing"
)

func importFixture() ImportPackage {
	return ImportPackage{
		SchemaVersion: 1,
		Sources: []ImportSource{
			{ID: "s1", URL: "https://example.org/data", License: "CC0-1.0", Version: "1"},
		},
		Dictionaries: []ImportDictionary{
			{
				Code:    "d1",
				Kind:    KindOpen,
				Release: 1,
				Entries: []DeliveryEntry{
					{Canonical: "Первое", Metadata: json.RawMessage(`{"directions":["d1"],"order":7}`)},
				},
			},
		},
	}
}

func TestPrepareImportPreservesProvenanceAndCanonicalChecksum(t *testing.T) {
	input := importFixture()
	first, err := PrepareImport(input)
	if err != nil {
		t.Fatal(err)
	}
	entry := first[0].Delivery.Entries[0]
	if entry.Status != StatusCandidate {
		t.Fatal("импорт не подтверждает значение автоматически", entry)
	}
	var metadata map[string]any
	if err := json.Unmarshal(entry.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["sourceId"] != "s1" || metadata["sourceUrl"] != "https://example.org/data" ||
		metadata["sourceLicense"] != "CC0-1.0" || metadata["sourceVersion"] != "1" || metadata["order"] != float64(7) {
		t.Fatal(metadata)
	}
	input.Dictionaries[0].Entries[0].Metadata = json.RawMessage(`{ "order": 7, "directions": ["d1"] }`)
	again, err := PrepareImport(input)
	if err != nil || again[0].Delivery.Checksum != first[0].Delivery.Checksum {
		t.Fatal("порядок ключей и пробелы меняют сумму", again, err)
	}
	if string(importFixture().Dictionaries[0].Entries[0].Metadata) !=
		`{"directions":["d1"],"order":7}` {
		t.Fatal("проверка изменила вход")
	}
}

func TestPrepareImportRejectsWholeInvalidPackage(t *testing.T) {
	cases := map[string]func(*ImportPackage){
		"версия":             func(p *ImportPackage) { p.SchemaVersion = 2 },
		"без источника":      func(p *ImportPackage) { p.Sources = nil },
		"http":               func(p *ImportPackage) { p.Sources[0].URL = "http://example.org/data" },
		"учётные данные URL": func(p *ImportPackage) { p.Sources[0].URL = "https://user:password@example.org" },
		"без лицензии":       func(p *ImportPackage) { p.Sources[0].License = " " },
		"повтор источника":   func(p *ImportPackage) { p.Sources = append(p.Sources, p.Sources[0]) },
		"повтор словаря":     func(p *ImportPackage) { p.Dictionaries = append(p.Dictionaries, p.Dictionaries[0]) },
		"вид":                func(p *ImportPackage) { p.Dictionaries[0].Kind = "other" },
		"релиз":              func(p *ImportPackage) { p.Dictionaries[0].Release = 0 },
		"пустой словарь":     func(p *ImportPackage) { p.Dictionaries[0].Entries = nil },
		"статус":             func(p *ImportPackage) { p.Dictionaries[0].Entries[0].Status = StatusRejected },
		"неизвестный источник": func(p *ImportPackage) {
			p.Dictionaries[0].Entries[0].Metadata = json.RawMessage(`{"sourceId":"missing"}`)
		},
		"источник не строка": func(p *ImportPackage) {
			p.Dictionaries[0].Entries[0].Metadata = json.RawMessage(`{"sourceId":1}`)
		},
		"неоднозначный источник": func(p *ImportPackage) {
			p.Sources = append(p.Sources, ImportSource{ID: "s2", URL: "https://example.org/second", License: "MIT"})
		},
		"коллизия нормализатора": func(p *ImportPackage) {
			p.Dictionaries[0].Entries = []DeliveryEntry{{Canonical: "Ёж"}, {Canonical: "Еж"}}
		},
		"коллизия варианта": func(p *ImportPackage) {
			p.Dictionaries[0].Entries = []DeliveryEntry{
				{Canonical: "Первое", Aliases: []DeliveryAlias{{Text: "Second"}}},
				{Canonical: "Second"},
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			input := importFixture()
			change(&input)
			if prepared, err := PrepareImport(input); err == nil || prepared != nil {
				t.Fatal("некорректный пакет прошёл", prepared, err)
			}
		})
	}
}

func TestPrepareImportDoesNotTrustUndeclaredSourceVersion(t *testing.T) {
	input := importFixture()
	input.Sources[0].Version = ""
	input.Dictionaries[0].Entries[0].Metadata = json.RawMessage(`{"sourceVersion":"forged"}`)
	prepared, err := PrepareImport(input)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(prepared[0].Delivery.Entries[0].Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if _, exists := metadata["sourceVersion"]; exists {
		t.Fatal("версия источника взята из непроверенного поля записи", metadata)
	}
}
