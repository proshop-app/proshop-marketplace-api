package taxonomyfile_test

import (
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/interfaces/taxonomyfile"
)

const valid = "- code: a1\n  name: Поле\n  type: enum\n  dictionary: d1\n"

func TestStrictYAML(t *testing.T) {
	d, err := taxonomyfile.DecodePart(strings.NewReader(valid), "attributes")
	if err != nil || len(d.Attributes) != 1 {
		t.Fatalf("%+v %v", d, err)
	}
	for _, raw := range []string{
		valid + "  extra: true\n",
		valid + "  values: [a]\n",
		valid + "  required: true\n",
		valid + "---\n" + valid,
		valid + "  code: a2\n",
		"- &field {code: a1, name: Поле, type: string}\n",
		"- &field {code: a1, name: Поле, type: string}\n- *field\n",
		"",
	} {
		if _, err := taxonomyfile.DecodePart(strings.NewReader(raw), "attributes"); err == nil {
			t.Fatal("принят неверный YAML", raw)
		}
	}
	body, err := taxonomyfile.EncodePart(d, "attributes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := taxonomyfile.DecodePart(strings.NewReader(string(body)), "attributes"); err != nil {
		t.Fatal(err)
	}
	if _, err := taxonomyfile.DecodePart(strings.NewReader(valid), "unknown"); err == nil {
		t.Fatal("неизвестная часть")
	}
}

func TestHintRulesYAMLRoundTripPreservesExplicitDisable(t *testing.T) {
	body := "- categoryId: c1\n  attributeCode: a1\n  mode: replace\n  dictionaries: []\n"
	d, err := taxonomyfile.DecodePart(strings.NewReader(body), "dictionary-bindings")
	if err != nil || len(d.DictionaryBindings) != 1 || d.DictionaryBindings[0].Dictionaries == nil {
		t.Fatal(d, err)
	}
	encoded, err := taxonomyfile.EncodePart(d, "dictionary-bindings")
	if err != nil {
		t.Fatal(err)
	}
	back, err := taxonomyfile.DecodePart(strings.NewReader(string(encoded)), "dictionary-bindings")
	if err != nil || back.DictionaryBindings[0].Mode != "replace" || back.DictionaryBindings[0].Dictionaries == nil {
		t.Fatal(back, err)
	}
}

func TestSearchTermsYAMLRoundTrip(t *testing.T) {
	body := "- code: a1\n  name: Поле\n  type: boolean\n" +
		"  searchTermsByValue:\n    'true': [включённый]\n    'false': []\n"
	d, err := taxonomyfile.DecodePart(strings.NewReader(body), "attributes")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := taxonomyfile.EncodePart(d, "attributes")
	if err != nil {
		t.Fatal(err)
	}
	back, err := taxonomyfile.DecodePart(strings.NewReader(string(encoded)), "attributes")
	if err != nil || back.Attributes[0].SearchTermsByValue["true"][0] != "включённый" {
		t.Fatal(back, err)
	}
	terms, ok := back.Attributes[0].SearchTermsByValue["false"]
	if !ok || terms == nil || len(terms) != 0 {
		t.Fatal("явный пустой список потерян", back)
	}
}
