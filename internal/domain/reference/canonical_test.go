package reference

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAuthorCanonicalSpelling(t *testing.T) {
	dict := testDictionary().State()
	dict.Code = russianAuthorDictionary
	authors := RestoreDictionary(dict)
	cases := map[string]bool{
		"Автор":                  true,
		"  АВТОР   Ё.  ":         true,
		"Джон Смит":              true,
		"Автор-2, «А.»":          true,
		"Foreign Author":         false,
		"Автор A":                false,
		"Автор І":                false,
		"Автор Ω":                false,
		"Автор 著":                false,
		"123 . —":                false,
		"":                       false,
		strings.Repeat("А", 121): false,
		"Автор\x00":              false,
	}

	for name, allowed := range cases {
		t.Run(name, func(t *testing.T) {
			clean, key, err := authors.CanonicalSpelling(name)

			if (err == nil) != allowed {
				t.Fatalf("canonical=%q, clean=%q, key=%q, err=%v", name, clean, key, err)
			}
		})
	}

	if clean, key, err := authors.CanonicalSpelling("  АВТОР   Ё.  "); err != nil ||
		clean != "АВТОР Ё." || key != "автор е." {
		t.Fatal(clean, key, err)
	}
}

func TestAuthorEntryRejectsForeignCanonicalButKeepsAliases(t *testing.T) {
	state := testDictionary().State()
	state.Code = russianAuthorDictionary
	dict := RestoreDictionary(state)
	input := NewEntryInput{
		ID:               "e1",
		CanonicalAliasID: "a1",
		Name:             "Автор",
		Status:           StatusCandidate,
		Aliases: []AliasInput{
			{
				ID:   "a2",
				Text: "Foreign Author",
				Kind: AliasLocale,
			},
		},
	}
	entry, err := NewEntry(dict, input, testNow)

	if err != nil {
		t.Fatal(err)
	}

	before := entry.State()

	if _, found := entry.Alias("foreign author"); !found {
		t.Fatal("иноязычный alias потерян")
	}

	if changed, err := entry.Rename(dict, "Foreign Author", "a3", testNow); changed || !errors.Is(err, ErrInvalid) {
		t.Fatal(changed, err)
	}

	if !reflect.DeepEqual(before, entry.State()) {
		t.Fatal("отказ переименования изменил запись")
	}

	input.Name = "Foreign Author"

	if _, err := NewEntry(dict, input, testNow); !errors.Is(err, ErrInvalid) {
		t.Fatal("иноязычный canonical принят", err)
	}

	if _, err := NewEntry(testDictionary(), input, testNow); err != nil {
		t.Fatal("другой словарь ограничен правилом авторов", err)
	}
}

func TestAuthorDeliveryChecksCanonicalSeparatelyFromAliases(t *testing.T) {
	delivery := Delivery{
		Dictionary: russianAuthorDictionary,
		Version:    1,
		Checksum:   testChecksum,
		Entries: []DeliveryEntry{
			{
				Canonical: "Автор",
				Status:    StatusCandidate,
				Aliases: []DeliveryAlias{
					{
						Text: "Foreign Author",
						Kind: AliasLocale,
					},
				},
			},
		},
	}

	if err := ValidateDelivery(delivery); err != nil {
		t.Fatal("поставка русского canonical с иноязычным alias отклонена", err)
	}

	delivery.Entries[0].Canonical = "Foreign Author"

	if err := ValidateDelivery(delivery); !errors.Is(err, ErrInvalidDelivery) {
		t.Fatal("поставка иноязычного canonical принята", err)
	}

	delivery.Dictionary = "b1"

	if err := ValidateDelivery(delivery); err != nil {
		t.Fatal("другой словарь ограничен правилом авторов", err)
	}
}
