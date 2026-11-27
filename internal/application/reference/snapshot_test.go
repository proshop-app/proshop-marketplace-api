package reference

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

func readTestSnapshot(service *Service, since *int64) (Snapshot, error) {
	ctx := context.Background()
	result, err := service.Snapshot(ctx, testDict, since, "")
	for err == nil && result.NextCursor != "" {
		var page Snapshot
		page, err = service.Snapshot(ctx, testDict, nil, result.NextCursor)
		result.Entries = append(result.Entries, page.Entries...)
		result.Removed = append(result.Removed, page.Removed...)
		result.NextCursor = page.NextCursor
	}
	return result, err
}

func TestSnapshotPagesKeepFirstRevisionAfterWrites(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var last EntryResult
	for i := range snapshotPageSize + 1 {
		last = h.create(t, fmt.Sprintf("Запись %d", i), reference.StatusConfirmed, fmt.Sprintf("Value %d", i))
	}
	since := int64(0)
	full, err := h.service.Snapshot(ctx, testDict, nil, "")
	if err != nil || len(full.Entries) != snapshotPageSize || full.NextCursor == "" {
		t.Fatal(len(full.Entries), full.NextCursor, err)
	}
	delta, err := h.service.Snapshot(ctx, testDict, &since, "")
	if err != nil || delta.Full || len(delta.Entries) != snapshotPageSize || delta.NextCursor == "" {
		t.Fatal(len(delta.Entries), delta.NextCursor, err)
	}
	newName := "Новое имя"
	if _, err := h.service.Update(ctx, UpdateCommand{
		Dictionary: testDict,
		ID:         last.Entry.ID,
		IfMatch:    last.Revision,
		Name:       &newName,
		Meta:       operatorMeta,
	}); err != nil {
		t.Fatal(err)
	}
	h.create(t, "Следующая запись", reference.StatusConfirmed)
	for _, first := range []Snapshot{full, delta} {
		next, err := h.service.Snapshot(ctx, testDict, nil, first.NextCursor)
		if err != nil || next.Revision != first.Revision || next.Full != first.Full || next.NextCursor != "" ||
			len(next.Entries) != 1 || next.Entries[0].CanonicalName != last.Entry.CanonicalName ||
			len(next.Entries[0].Aliases) == 0 {
			t.Fatal(next, err)
		}
	}
	_, err = h.service.Snapshot(ctx, testDict, &since, delta.NextCursor)
	if !errors.Is(err, reference.ErrInvalid) {
		t.Fatal(err)
	}
	_, err = h.service.Snapshot(ctx, "other", nil, full.NextCursor)
	if !errors.Is(err, reference.ErrInvalid) {
		t.Fatal(err)
	}
}
