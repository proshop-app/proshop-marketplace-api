package taxonomydata

import (
	"bytes"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestReviewListsEveryLeafOnce(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := WriteReview(&output, release, ""); err != nil {
		t.Fatal(err)
	}
	var check func([]taxonomy.Category)
	check = func(categories []taxonomy.Category) {
		for _, category := range categories {
			if len(category.Children) == 0 && strings.Count(output.String(), "("+category.ID+")") != 1 {
				t.Errorf("лист отчёта пропущен или повторён: %s", category.ID)
			}
			check(category.Children)
		}
	}
	check(release.Categories)
	if err := WriteReview(&output, release, "missing"); err == nil {
		t.Fatal("принято несуществующее направление")
	}
}
