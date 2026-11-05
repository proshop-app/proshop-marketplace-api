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
