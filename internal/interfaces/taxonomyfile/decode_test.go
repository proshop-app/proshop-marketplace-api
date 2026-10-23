package taxonomyfile_test

import (
	"os"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/interfaces/taxonomyfile"
)

const valid = "version: 1\ncategories:\n  - id: c1\n    name: Категория\n"

func TestStrictYAML(t *testing.T) {
	if d, err := taxonomyfile.Decode(strings.NewReader(valid)); err != nil || d.Version != 1 {
		t.Fatalf("%+v %v", d, err)
	}
	for _, raw := range []string{
		valid + "extra: true\n",
		valid + "---\n" + valid,
		strings.Replace(valid, "version: 1", "version: 1\nversion: 2", 1),
		"",
	} {
		if _, err := taxonomyfile.Decode(strings.NewReader(raw)); err == nil {
			t.Fatal("принят неверный YAML", raw)
		}
	}
}

func TestExampleFile(t *testing.T) {
	file, err := os.Open("../../../taxonomy/example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := taxonomyfile.Decode(file); err != nil {
		t.Fatal(err)
	}
}
