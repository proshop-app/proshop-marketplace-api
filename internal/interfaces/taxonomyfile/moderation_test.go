package taxonomyfile_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/taxonomyfile"
)

func TestModerationYAML(t *testing.T) {
	source := "version: 1\ncategories:\n- id: c1\n  name: Категория\n  slug: c1\n  moderation: manual\n"
	d, err := taxonomyfile.Decode(strings.NewReader(source))
	if err != nil || d.ModerationPolicy("c1") != "manual" {
		t.Fatal(d, err)
	}
	_, err = taxonomyfile.Decode(strings.NewReader(strings.Replace(source, "manual", "unknown", 1)))
	if !errors.Is(err, taxonomy.ErrInvalid) {
		t.Fatal(err)
	}
}
