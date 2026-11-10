package reference_test

import (
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestReviewSelectionIsStrictAndBounded(t *testing.T) {
	id := ids.UUID("00000000-0000-0000-0000-000000000001")
	valid := []reference.ReviewSelection{
		{Mode: reference.ReviewIDs, IDs: []ids.UUID{id}},
		{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{}},
		{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{id}},
	}
	for _, selection := range valid {
		for _, target := range []reference.Status{reference.StatusConfirmed, reference.StatusRejected} {
			if err := reference.ValidateReview(target, selection); err != nil {
				t.Fatal(selection, target, err)
			}
		}
	}
	invalid := []reference.ReviewSelection{
		{},
		{Mode: reference.ReviewIDs},
		{Mode: reference.ReviewIDs, IDs: []ids.UUID{}},
		{Mode: reference.ReviewIDs, IDs: []ids.UUID{id}, ExcludedIDs: []ids.UUID{}},
		{Mode: reference.ReviewIDs, IDs: []ids.UUID{id, id}},
		{Mode: reference.ReviewIDs, IDs: []ids.UUID{"wrong"}},
		{Mode: reference.ReviewAllCandidates},
		{Mode: reference.ReviewAllCandidates, IDs: []ids.UUID{}, ExcludedIDs: []ids.UUID{}},
		{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{id, id}},
		{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{"wrong"}},
		{Mode: reference.ReviewIDs, IDs: make([]ids.UUID, reference.MaxReviewEntries+1)},
		{Mode: reference.ReviewAllCandidates, ExcludedIDs: make([]ids.UUID, reference.MaxReviewEntries+1)},
	}
	for _, selection := range invalid {
		if err := reference.ValidateReview(reference.StatusConfirmed, selection); err == nil {
			t.Fatal("неверный выбор принят", selection.Mode)
		}
	}
	if err := reference.ValidateReview(reference.StatusDeprecated, valid[0]); !errors.Is(err, reference.ErrInvalid) {
		t.Fatal("чужой переход принят", err)
	}
}
