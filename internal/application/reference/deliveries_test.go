package reference

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

func testImport(version int64, entries ...reference.DeliveryEntry) reference.ImportPackage {
	input := importPackageFixture()
	input.Dictionaries = input.Dictionaries[:1]
	dictionary := &input.Dictionaries[0]
	dictionary.Code = testDict
	dictionary.Identity = true
	dictionary.Release = version
	dictionary.Entries = entries
	return input
}

func TestImportIsIdempotentAndExtends(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	translit := []reference.DeliveryAlias{{Text: "Value", Kind: reference.AliasTranslit}}
	first := testImport(
		1,
		reference.DeliveryEntry{Canonical: "Значение", Status: reference.StatusConfirmed, Aliases: translit},
		reference.DeliveryEntry{
			Canonical: "Значение Два",
			Status:    reference.StatusCandidate,
			Metadata:  []byte(`{"directions":["d1"]}`),
		},
	)
	result, err := importOne(t, h, first)
	// одна пачка — одна ревизия журнала
	if err != nil || !result.Changed || result.CreatedEntries != 2 || result.Revision != 1 {
		t.Fatalf("поставка 1: %+v %v", result, err)
	}
	repeat, err := importOne(t, h, first)
	if err != nil || repeat.Changed || repeat.Revision != 1 {
		t.Fatalf("повтор поставки: %+v %v", repeat, err)
	}
	tampered := first
	tampered.Dictionaries = append([]reference.ImportDictionary(nil), first.Dictionaries...)
	tampered.Dictionaries[0].Entries = []reference.DeliveryEntry{{Canonical: "Подмена"}}
	if _, err := importOne(t, h, tampered); !errors.Is(err, reference.ErrDeliveryConflict) {
		t.Fatalf("подмена поставки: %v", err)
	}
	brand := []reference.DeliveryAlias{{Text: "Variant"}}
	second := testImport(
		2,
		reference.DeliveryEntry{Canonical: "значение", Status: reference.StatusConfirmed, Aliases: brand},
		reference.DeliveryEntry{Canonical: "Значение Два", Status: reference.StatusConfirmed},
	)
	result, err = importOne(t, h, second)
	if err != nil || result.CreatedEntries != 0 || result.UpdatedEntries != 2 {
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

func TestImportConflictsOnForeignKey(t *testing.T) {
	h := newHarness(t)
	h.create(t, "Значение", reference.StatusConfirmed, "Value")
	foreign := []reference.DeliveryAlias{{Text: "value"}}
	entry := reference.DeliveryEntry{Canonical: "Значение Два", Status: reference.StatusConfirmed, Aliases: foreign}
	delivery := testImport(1, entry)
	if _, err := importOne(t, h, delivery); !errors.Is(err, reference.ErrKeyTaken) {
		t.Fatalf("чужой ключ в поставке: %v", err)
	}
	if len(h.store.ReferenceChanges()) != 1 {
		t.Fatal("отказанная поставка оставила ревизии")
	}
}

func TestEventListsAreTruncated(t *testing.T) {
	h := newHarness(t)
	entries := make([]reference.DeliveryEntry, 0, maxEventList+1)
	for i := 0; i <= maxEventList; i++ {
		entry := reference.DeliveryEntry{Canonical: "b" + strconv.Itoa(i), Status: reference.StatusConfirmed}
		entries = append(entries, entry)
	}
	delivery := testImport(1, entries...)
	if _, err := importOne(t, h, delivery); err != nil {
		t.Fatal(err)
	}
	events := h.events(t)
	// 1001 запись — три пачки и три ревизии журнала, событие одно на транзакцию
	batches := (maxEventList + deliveryBatch) / deliveryBatch
	if len(events) != 1 || !events[0].Truncated || len(events[0].AffectedKeys) != maxEventList ||
		len(events[0].EntryIDs) != maxEventList || events[0].RevisionTo != int64(batches) ||
		len(h.store.ReferenceChanges()) != batches {
		t.Fatalf("событие поставки: %+v, журнал: %d", events[0].RevisionTo, len(h.store.ReferenceChanges()))
	}
}

func importOne(t *testing.T, h *harness, input reference.ImportPackage) (ImportResult, error) {
	t.Helper()
	results, err := h.service.Import(context.Background(), "", input)
	if err != nil {
		return ImportResult{}, err
	}
	return results[0], nil
}
