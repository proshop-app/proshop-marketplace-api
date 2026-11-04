package referencedata

import (
	"errors"
	"os"
	"testing"
	"testing/fstest"
)

func TestLoadChecksBundle(t *testing.T) {
	delivery, summary, err := Load(os.DirFS("testdata/b1"))
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Dictionary != "b1" || delivery.Version != 1 || summary.Confirmed != 1 || summary.Candidates != 1 ||
		summary.Aliases != 1 || len(delivery.Entries) != 2 {
		t.Fatalf("поставка: %+v %+v", delivery, summary)
	}
}

func TestLoadRejectsTamperedBundle(t *testing.T) {
	original := os.DirFS("testdata/b1")
	manifestBody, err := fsRead(original, manifestFile)
	if err != nil {
		t.Fatal(err)
	}
	entriesBody, err := fsRead(original, entriesFile)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]fstest.MapFS{
		"данные изменены": {
			manifestFile: {Data: manifestBody},
			entriesFile:  {Data: append(entriesBody, ' ')},
		},
		"нет данных": {
			manifestFile: {Data: manifestBody},
		},
		"лишнее поле": {
			manifestFile: {Data: []byte(`{"dictionary":"b1","release":1,"checksum":"x","extra":1}`)},
			entriesFile:  {Data: entriesBody},
		},
	}
	for name, source := range cases {
		if _, _, err := Load(source); !errors.Is(err, ErrInvalidBundle) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLoadRejectsInvalidDelivery(t *testing.T) {
	entries := []byte(`[{"canonical":"Бренд","status":"merged"}]`)
	header := `{"dictionary":"b1","release":1,"checksum":"` + checksumOf(entries) + `","entries":1,"aliases":0}`
	source := fstest.MapFS{
		manifestFile: {Data: []byte(header)},
		entriesFile:  {Data: entries},
	}
	if _, _, err := Load(source); !errors.Is(err, ErrInvalidBundle) {
		t.Fatalf("статус merged в поставке: %v", err)
	}
}
