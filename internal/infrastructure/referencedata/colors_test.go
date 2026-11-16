package referencedata

import (
	"os"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/colorpalette"
)

func TestColorDeliveryCoversPaletteAndShades(t *testing.T) {
	delivery, summary, err := Load(os.DirFS("../../../deploy/reference/color"))
	if err != nil {
		t.Fatal(err)
	}
	if len(delivery.Entries) != 38 || summary.Aliases != 2 {
		t.Fatalf("не покрыты 40 значений: %+v", summary)
	}
	bases, shades := 0, 0
	for _, entry := range delivery.Entries {
		parents, err := colorpalette.Parents(entry.Metadata)
		if err != nil {
			t.Fatal(err)
		}
		if colorpalette.IsBase(entry.Canonical) {
			bases++
		} else if len(parents) > 0 {
			shades++
		}
	}
	if bases != 18 || shades != 20 {
		t.Fatalf("палитра %d, оттенки %d", bases, shades)
	}
}
