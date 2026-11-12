package reference

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

func importPackageFixture() reference.ImportPackage {
	return reference.ImportPackage{
		SchemaVersion: 1,
		Sources: []reference.ImportSource{
			{ID: "s1", URL: "https://example.org/data", License: "CC0-1.0"},
		},
		Dictionaries: []reference.ImportDictionary{
			{
				Code:    "a1",
				Kind:    reference.KindOpen,
				Release: 1,
				Entries: []reference.DeliveryEntry{{Canonical: "Первое"}},
			},
			{
				Code:    "c1",
				Kind:    reference.KindClosed,
				Release: 1,
				Entries: []reference.DeliveryEntry{{Canonical: "Второе"}},
			},
		},
	}
}

func TestImportRecordsAuthorAndDeliveryRelease(t *testing.T) {
	for _, actor := range []string{"op1", ""} {
		name := actor
		if name == "" {
			name = "без оператора"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			input := importPackageFixture()
			input.Dictionaries[0].Release = 7
			input.Dictionaries[1].Release = 9
			results, err := h.service.Import(ctx, actor, input)
			if err != nil {
				t.Fatal(err)
			}
			changes := h.store.ReferenceChanges()
			if len(changes) != len(results) || len(results) != len(input.Dictionaries) {
				t.Fatal("не все словари попали в журнал", changes, results)
			}
			for i, result := range results {
				expectedActor := actor
				if expectedActor == "" {
					expectedActor = "delivery:" + strconv.FormatInt(result.Release, 10)
				}
				change := changes[i]
				if change.Dictionary != result.Code || change.Kind != reference.ChangeDelivered ||
					change.Meta.Actor != expectedActor || change.Meta.Source != reference.SourceSeed {
					t.Fatal("неверный автор или источник импорта", change)
				}
				if err := h.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
					delivery, err := tx.Reference().LatestDelivery(ctx, result.Code)
					if err != nil {
						return err
					}
					if delivery == nil || delivery.Version != result.Release ||
						delivery.RevisionFrom != 0 || delivery.RevisionTo != result.Revision {
						t.Fatal("потерян релиз или диапазон ревизий поставки", delivery, result)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestApplyWithoutOperatorKeepsDeliveryAuthor(t *testing.T) {
	h := newHarness(t)
	delivery := testDelivery(
		7,
		strings.Repeat("a", 64),
		reference.DeliveryEntry{Canonical: "Значение", Status: reference.StatusCandidate},
	)
	if _, err := h.service.Apply(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	changes := h.store.ReferenceChanges()
	if len(changes) != 1 || changes[0].Meta.Actor != "delivery:7" ||
		changes[0].Meta.Source != reference.SourceSeed {
		t.Fatal("автор поставки без оператора изменился", changes)
	}
}

func TestImportCreatesMixedDictionariesAndNoopsWithoutNewEvents(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	input := importPackageFixture()
	first, err := h.service.Import(ctx, "", input)
	if err != nil || len(first) != 2 {
		t.Fatal(first, err)
	}
	for _, result := range first {
		if !result.Created || !result.Changed || result.CreatedEntries != 1 || result.Revision < 1 {
			t.Fatal(result)
		}
	}
	summaries, err := h.service.DictionarySummaries(ctx)
	if err != nil || len(summaries) != 3 || summaries[0].Candidates != 1 ||
		summaries[2].Kind != reference.KindClosed || summaries[2].Candidates != 1 {
		t.Fatal(summaries, err)
	}
	before := len(h.events(t))
	again, err := h.service.Import(ctx, "", input)
	if err != nil || len(again) != 2 || len(h.events(t)) != before {
		t.Fatal(again, err)
	}
	for _, result := range again {
		if result.Created || result.Changed || result.CreatedEntries != 0 {
			t.Fatal("повтор изменил реестр", result)
		}
	}
	input.Dictionaries[0].Entries[0].Canonical = "Подмена"
	if _, err := h.service.Import(ctx, "", input); !errors.Is(err, reference.ErrDeliveryConflict) {
		t.Fatal("подмена релиза не отклонена", err)
	}
}

func TestImportRollsBackEarlierDictionaryJournalAndOutboxOnConflict(t *testing.T) {
	h := newHarness(t)
	h.create(t, "Владелец", reference.StatusCandidate, "Занято")
	before := len(h.events(t))
	input := importPackageFixture()
	input.Dictionaries[1].Code = testDict
	input.Dictionaries[1].Kind = reference.KindOpen
	input.Dictionaries[1].Entries[0].Aliases = []reference.DeliveryAlias{{Text: "Занято"}}
	if result, err := h.service.Import(context.Background(), "", input); !errors.Is(err, reference.ErrKeyTaken) ||
		result != nil {
		t.Fatal(result, err)
	}
	dictionaries, err := h.service.Dictionaries(context.Background())
	if err != nil || len(dictionaries) != 1 || len(h.events(t)) != before {
		t.Fatal("первый словарь или его событие пережили откат", dictionaries, err)
	}
}

func TestImportRejectsKindAndNormalizerChangesAtomically(t *testing.T) {
	for _, normalizer := range []bool{false, true} {
		t.Run(map[bool]string{false: "kind", true: "normalizer"}[normalizer], func(t *testing.T) {
			h := newHarness(t)
			input := importPackageFixture()
			input.Dictionaries[1].Code = testDict
			if normalizer {
				limits := reference.DefaultLimits()
				limits.Normalizer = "v2"
				h.store.SeedReferenceDictionary(reference.DictionaryState{
					Code:       testDict,
					Kind:       reference.KindClosed,
					AliasKinds: reference.AliasKinds,
					Limits:     limits,
				})
			}
			if _, err := h.service.Import(context.Background(), "", input); err == nil {
				t.Fatal("несовместимый словарь принят")
			}
			dictionaries, err := h.service.Dictionaries(context.Background())
			if err != nil || len(dictionaries) != 1 || len(h.events(t)) != 0 {
				t.Fatal("частичное применение", dictionaries, err)
			}
		})
	}
}

func TestImportPreservesOperatorMetadataAndRejectedEntry(t *testing.T) {
	h := newHarness(t)
	created := h.create(t, "Запись", reference.StatusCandidate)
	metadata := json.RawMessage(`{"operator":"keep"}`)
	status := reference.StatusRejected
	updated, err := h.service.Update(context.Background(), UpdateCommand{
		Dictionary: testDict,
		ID:         created.Entry.ID,
		IfMatch:    created.Entry.Revision,
		Metadata:   metadata,
		Status:     &status,
		Meta:       operatorMeta,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := importPackageFixture()
	input.Dictionaries = []reference.ImportDictionary{
		{
			Code:    testDict,
			Kind:    reference.KindOpen,
			Release: 1,
			Entries: []reference.DeliveryEntry{{Canonical: "Запись", Status: reference.StatusConfirmed}},
		},
	}
	result, err := h.service.Import(context.Background(), "", input)
	if err != nil || result[0].SkippedEntries != 1 {
		t.Fatal(result, err)
	}
	entry, err := h.service.Get(context.Background(), testDict, created.Entry.ID)
	if err != nil || entry.Entry.Status != reference.StatusRejected ||
		string(entry.Entry.Metadata) != string(metadata) ||
		entry.Entry.Revision != updated.Entry.Revision {
		t.Fatal("решение оператора изменено", entry, err)
	}
}
