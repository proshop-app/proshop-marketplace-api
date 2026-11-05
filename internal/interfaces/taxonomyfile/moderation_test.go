package taxonomyfile_test

import (
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/interfaces/taxonomyfile"
)

// Политика узла читается из части categories; допустимые значения проверяет домен.
func TestModerationYAML(t *testing.T) {
	source := "- id: c1\n  name: Категория\n  slug: c1\n  moderation: manual\n"
	d, err := taxonomyfile.DecodePart(strings.NewReader(source), "categories")
	if err != nil || d.ModerationPolicy("c1") != "manual" {
		t.Fatal(d, err)
	}
}
