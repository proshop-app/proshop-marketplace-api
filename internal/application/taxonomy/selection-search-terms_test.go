package taxonomy

import (
	"slices"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

func TestSelectionSearchTermsKeepLabelAndHighestWeightedAliases(t *testing.T) {
	entry := reference.EntryState{
		CanonicalName: "46",
		Aliases: []reference.AliasState{
			{Text: "v6", Weight: 5},
			{Text: "XL", Weight: 100},
			{Text: "v4", Weight: 20},
			{Text: "v1", Weight: 60},
			{Text: "v7", Weight: 5},
			{Text: "v8", Weight: 3},
			{Text: "v5", Weight: 10},
			{Text: "v3", Weight: 30},
			{Text: "v2", Weight: 40},
			{Text: "46", Weight: 100},
			{Text: strings.Repeat("щ", 129), Weight: 100},
		},
	}
	want := []string{"XL", "v1", "v2", "v3", "v4", "v5", "v6", "v7"}
	if got := selectionSearchTerms("d1", entry, "XL"); !slices.Equal(got, want) {
		t.Fatalf("выражения: %v, ожидались %v", got, want)
	}
	slices.Reverse(entry.Aliases)
	if got := selectionSearchTerms("d1", entry, "XL"); !slices.Equal(got, want) {
		t.Fatalf("порядок списка алиасов повлиял на отбор: %v", got)
	}
}
