package reference

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

func testDelivery(version int64, checksum string, entries ...reference.DeliveryEntry) reference.Delivery {
	return reference.Delivery{Dictionary: testDict, Version: version, Checksum: checksum, Entries: entries}
}

func TestApplyIsIdempotentAndExtends(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	translit := []reference.DeliveryAlias{{Text: "Brend", Kind: reference.AliasTranslit}}
	first := testDelivery(
		1,
		strings.Repeat("a", 64),
		reference.DeliveryEntry{Canonical: "Бренд", Status: reference.StatusConfirmed, Aliases: translit},
		reference.DeliveryEntry{
			Canonical: "Бренд Два",
			Status:    reference.StatusCandidate,
			Metadata:  []byte(`{"directions":["d1"]}`),
		},
	)
	result, err := h.service.Apply(ctx, first)
	if err != nil || !result.Changed || result.Created != 2 || result.Revision != 2 {
		t.Fatalf("поставка 1: %+v %v", result, err)
	}
	repeat, err := h.service.Apply(ctx, first)
	if err != nil || repeat.Changed || repeat.Revision != 2 {
		t.Fatalf("повтор поставки: %+v %v", repeat, err)
	}
	tampered := first
	tampered.Checksum = strings.Repeat("b", 64)
	if _, err := h.service.Apply(ctx, tampered); !errors.Is(err, reference.ErrDeliveryConflict) {
		t.Fatalf("подмена поставки: %v", err)
	}
	brand := []reference.DeliveryAlias{{Text: "Brand"}}
	second := testDelivery(
		2,
		strings.Repeat("c", 64),
		reference.DeliveryEntry{Canonical: "бренд", Status: reference.StatusConfirmed, Aliases: brand},
		reference.DeliveryEntry{Canonical: "Бренд Два", Status: reference.StatusConfirmed},
	)
	result, err = h.service.Apply(ctx, second)
	if err != nil || result.Created != 0 || result.Updated != 2 {
		t.Fatalf("поставка 2: %+v %v", result, err)
	}
	snapshot, _ := h.service.Snapshot(ctx, testDict, nil)
	if len(snapshot.Entries) != 2 {
		t.Fatalf("дубли после поставки: %+v", snapshot.Entries)
	}
	for _, entry := range snapshot.Entries {
		if entry.Status != reference.StatusConfirmed || entry.Source != reference.SourceSeed {
			t.Fatalf("запись поставки: %+v", entry)
		}
	}
}

func TestApplyConflictsOnForeignKey(t *testing.T) {
	h := newHarness(t)
	h.create(t, "Бренд", reference.StatusConfirmed, "Brend")
	foreign := []reference.DeliveryAlias{{Text: "brend"}}
	entry := reference.DeliveryEntry{Canonical: "Бренд Два", Status: reference.StatusConfirmed, Aliases: foreign}
	delivery := testDelivery(1, strings.Repeat("a", 64), entry)
	if _, err := h.service.Apply(context.Background(), delivery); !errors.Is(err, reference.ErrKeyTaken) {
		t.Fatalf("чужой ключ в поставке: %v", err)
	}
	if len(h.store.ReferenceChanges()) != 1 {
		t.Fatal("отказанная поставка оставила ревизии")
	}
}

func TestRollbackRestoresAndRefusesAfterEdits(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	existing := h.create(t, "Бренд", reference.StatusCandidate)
	aliases := []reference.DeliveryAlias{{Text: "Brend"}}
	delivery := testDelivery(
		1,
		strings.Repeat("a", 64),
		reference.DeliveryEntry{Canonical: "Бренд", Status: reference.StatusConfirmed, Aliases: aliases},
		reference.DeliveryEntry{Canonical: "Бренд Два", Status: reference.StatusConfirmed},
	)
	if _, err := h.service.Apply(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	rolled, err := h.service.Rollback(ctx, delivery)
	if err != nil || !rolled.Changed || rolled.Updated != 2 {
		t.Fatalf("откат: %+v %v", rolled, err)
	}
	snapshot, _ := h.service.Snapshot(ctx, testDict, nil)
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Status != reference.StatusCandidate ||
		len(snapshot.Entries[0].Aliases) != 1 {
		t.Fatalf("после отката: %+v", snapshot.Entries)
	}
	since := int64(3)
	delta, _ := h.service.Snapshot(ctx, testDict, &since)
	if len(delta.Removed) != 1 || len(delta.Entries) != 1 || delta.Entries[0].ID != existing.Entry.ID {
		t.Fatalf("дельта отката: %+v", delta)
	}
	if repeat, err := h.service.Rollback(ctx, delivery); err != nil || repeat.Changed {
		t.Fatalf("повтор отката: %+v %v", repeat, err)
	}
	if _, err := h.service.Apply(ctx, delivery); !errors.Is(err, reference.ErrDeliveryConflict) {
		t.Fatalf("повтор откаченной поставки: %v", err)
	}

	third := reference.DeliveryEntry{Canonical: "Бренд Три", Status: reference.StatusConfirmed}
	next := testDelivery(2, strings.Repeat("b", 64), third)
	if _, err := h.service.Apply(ctx, next); err != nil {
		t.Fatal(err)
	}
	h.create(t, "Правка", reference.StatusConfirmed)
	if _, err := h.service.Rollback(ctx, next); !errors.Is(err, reference.ErrDeliveryConflict) {
		t.Fatalf("откат после правок: %v", err)
	}
}

func TestRevertMergeByInverse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, "Бренд", reference.StatusConfirmed, "Brend")
	b := h.create(t, "Бренд Два", reference.StatusConfirmed)
	before, _ := h.service.Snapshot(ctx, testDict, nil)
	if _, err := h.service.Merge(ctx, MergeCommand{
		Dictionary: testDict,
		ID:         a.Entry.ID,
		Into:       b.Entry.ID,
		IfMatch:    a.Entry.Revision,
		Meta:       operatorMeta,
	}); err != nil {
		t.Fatal(err)
	}
	changes := h.store.ReferenceChanges()
	merge := changes[len(changes)-1]
	if _, err := h.service.mutate(ctx, testDict, func(ctx context.Context, m *session) error {
		return h.service.revert(ctx, m, merge, operatorMeta)
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := h.service.Snapshot(ctx, testDict, nil)
	for i := range before.Entries {
		if !reference.SameContent(&before.Entries[i], &after.Entries[i]) {
			t.Fatalf("откат объединения: %+v → %+v", before.Entries[i], after.Entries[i])
		}
	}
	last := h.events(t)[len(h.events(t))-1]
	if last.Kinds[len(last.Kinds)-1] != reference.EventMerged {
		t.Fatalf("событие отката: %+v", last)
	}
	if _, err := h.service.mutate(ctx, testDict, func(ctx context.Context, m *session) error {
		return h.service.revert(ctx, m, merge, operatorMeta)
	}); !errors.Is(err, reference.ErrRevertConflict) {
		t.Fatalf("повторный откат той же ревизии: %v", err)
	}
}

func TestEventListsAreTruncated(t *testing.T) {
	h := newHarness(t)
	entries := make([]reference.DeliveryEntry, 0, maxEventList+1)
	for i := 0; i <= maxEventList; i++ {
		entry := reference.DeliveryEntry{Canonical: "b" + strconv.Itoa(i), Status: reference.StatusConfirmed}
		entries = append(entries, entry)
	}
	delivery := testDelivery(1, strings.Repeat("a", 64), entries...)
	if _, err := h.service.Apply(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	events := h.events(t)
	if len(events) != 1 || !events[0].Truncated || len(events[0].AffectedKeys) != maxEventList ||
		len(events[0].EntryIDs) != maxEventList || events[0].RevisionTo != int64(maxEventList+1) {
		t.Fatalf("событие поставки: truncated=%v keys=%d", events[0].Truncated, len(events[0].AffectedKeys))
	}
}
