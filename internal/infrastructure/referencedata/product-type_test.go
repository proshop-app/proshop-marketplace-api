package referencedata

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

func TestProductTypeDictionary(t *testing.T) {
	input, err := LoadImport(os.DirFS("../../../deploy/reference"), "bootstrap.json")
	if err != nil {
		t.Fatal(err)
	}
	var dictionary reference.ImportDictionary
	for _, item := range input.Dictionaries {
		if item.Code == "product_type" {
			dictionary = item
			break
		}
	}
	if dictionary.Kind != reference.KindClosed || dictionary.Release != 1 {
		t.Fatalf("неверный заголовок типа: %+v", dictionary)
	}
	if len(dictionary.Entries) != 154 {
		t.Fatalf("значений: %d", len(dictionary.Entries))
	}
	seen := map[string]bool{}
	previous := ""
	for i, entry := range dictionary.Entries {
		value := entry.Canonical
		if seen[value] || strings.Contains(value, " и ") || strings.Contains(value, ",") ||
			entry.Status != "confirmed" {
			t.Fatalf("не отдельный подтверждённый тип: %+v", entry)
		}
		seen[value] = true
		var meta struct {
			Order int `json:"order"`
		}
		if err := json.Unmarshal(entry.Metadata, &meta); err != nil {
			t.Fatal(err)
		}
		key := strings.ReplaceAll(strings.ToLower(value), "ё", "е")
		if meta.Order != i || key <= previous {
			t.Fatalf("не алфавитный порядок: %s", value)
		}
		previous = key
	}
}
