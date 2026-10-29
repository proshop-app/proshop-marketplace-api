package reference

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

func TestCreateWritesJournalAndEvent(t *testing.T) {
	h := newHarness(t)
	result := h.create(t, "Бренд", reference.StatusConfirmed, "Brend")
	if !result.Created || result.Revision != 1 || result.Entry.Revision != 1 || len(result.Entry.Aliases) != 2 {
		t.Fatalf("результат: %+v", result)
	}
	changes := h.store.ReferenceChanges()
	if len(changes) != 1 || changes[0].Kind != reference.ChangeCreated || changes[0].Inverse[0].State != nil {
		t.Fatalf("журнал: %+v", changes)
	}
	events := h.events(t)
	want := Event{
		SchemaVersion: 1,
		Dictionary:    testDict,
		RevisionFrom:  0,
		RevisionTo:    1,
		EntryIDs:      changes[0].EntryIDs,
		Kinds:         []reference.EventKind{reference.EventAliasAdded},
		AffectedKeys:  []string{"brend", "бренд"},
	}
	if len(events) != 1 || !reflect.DeepEqual(events[0], want) {
		t.Fatalf("событие: %+v", events)
	}
}

func TestRepeatedCandidateCountsMeetings(t *testing.T) {
	h := newHarness(t)
	first := h.create(t, "Бренд", reference.StatusCandidate)
	second := h.create(t, " БРЕНД ", reference.StatusCandidate)
	if second.Created || second.Changed || second.Entry.ID != first.Entry.ID || second.Revision != 1 {
		t.Fatalf("повтор кандидата: %+v", second)
	}
	stored, _ := h.store.ReferenceEntry(first.Entry.ID)
	if stored.Occurrences != 2 || len(h.store.ReferenceChanges()) != 1 {
		t.Fatalf("встречи: %d, журнал: %d", stored.Occurrences, len(h.store.ReferenceChanges()))
	}
	for _, cmd := range []CreateCommand{
		{Dictionary: testDict, Name: "бренд", Status: reference.StatusCandidate, Aliases: []AliasSpec{{Text: "Brend"}}},
		{
			Dictionary: testDict,
			Name:       "бренд",
			Status:     reference.StatusCandidate,
			Metadata:   []byte(`{"directions":["d1"]}`),
		},
	} {
		cmd.Meta = operatorMeta
		if _, err := h.service.Create(context.Background(), cmd); !errors.Is(err, reference.ErrKeyTaken) {
			t.Fatalf("кандидат с данными на занятом ключе: %v", err)
		}
	}
	stored, _ = h.store.ReferenceEntry(first.Entry.ID)
	if stored.Occurrences != 2 || len(stored.Aliases) != 1 {
		t.Fatalf("отказ изменил владельца ключа: %+v", stored)
	}
}

func TestTakenKeyIsConflict(t *testing.T) {
	h := newHarness(t)
	h.create(t, "Бренд", reference.StatusConfirmed, "Brend")
	ctx := context.Background()
	_, err := h.service.Create(ctx, CreateCommand{
		Dictionary: testDict,
		Name:       "бренд",
		Status:     reference.StatusConfirmed,
		Meta:       operatorMeta,
	})
	if !errors.Is(err, reference.ErrKeyTaken) {
		t.Fatalf("занятое каноническое имя: %v", err)
	}
	_, err = h.service.Create(ctx, CreateCommand{
		Dictionary: testDict,
		Name:       "Бренд Два",
		Status:     reference.StatusConfirmed,
		Aliases:    []AliasSpec{{Text: "BREND"}},
		Meta:       operatorMeta,
	})
	if !errors.Is(err, reference.ErrKeyTaken) {
		t.Fatalf("занятый вариант: %v", err)
	}
	if len(h.store.ReferenceChanges()) != 1 || len(h.events(t)) != 1 {
		t.Fatal("отказ оставил журнал или событие")
	}
}

func TestUpdateRequiresCurrentRevision(t *testing.T) {
	h := newHarness(t)
	created := h.create(t, "Бренд", reference.StatusCandidate)
	ctx := context.Background()
	name := "Бренд Два"
	_, err := h.service.Update(ctx, UpdateCommand{
		Dictionary: testDict,
		ID:         created.Entry.ID,
		IfMatch:    created.Entry.Revision + 1,
		Name:       &name,
		Meta:       operatorMeta,
	})
	if !errors.Is(err, reference.ErrEntryChanged) {
		t.Fatalf("устаревший If-Match: %v", err)
	}
	updated, err := h.service.Update(ctx, UpdateCommand{
		Dictionary: testDict,
		ID:         created.Entry.ID,
		IfMatch:    created.Entry.Revision,
		Name:       &name,
		Meta:       operatorMeta,
	})
	if err != nil || updated.Entry.CanonicalKey != "бренд два" || updated.Revision != 2 {
		t.Fatalf("переименование: %+v %v", updated, err)
	}
	rejected := reference.StatusRejected
	result, err := h.service.Update(ctx, UpdateCommand{
		Dictionary: testDict,
		ID:         created.Entry.ID,
		IfMatch:    updated.Entry.Revision,
		Status:     &rejected,
		Meta:       operatorMeta,
	})
	if err != nil || result.Entry.Status != reference.StatusRejected {
		t.Fatalf("отклонение: %+v %v", result, err)
	}
	events := h.events(t)
	kinds := []reference.EventKind{events[1].Kinds[0], events[2].Kinds[0]}
	if !reflect.DeepEqual(kinds, []reference.EventKind{reference.EventAliasAdded, reference.EventStatusChanged}) ||
		!reflect.DeepEqual(events[1].Kinds, []reference.EventKind{reference.EventAliasAdded, reference.EventRenamed}) {
		t.Fatalf("события: %+v", events)
	}
	again := h.create(t, "бренд", reference.StatusCandidate)
	if again.Created || again.Entry.Status != reference.StatusRejected {
		t.Fatalf("отклонённый ключ завёлся заново: %+v", again)
	}
}

func TestMergeMovesAliasesAndCollapsesChain(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.create(t, "Бренд", reference.StatusConfirmed, "Brend")
	b := h.create(t, "Бренд Два", reference.StatusConfirmed)
	c := h.create(t, "Бренд Три", reference.StatusConfirmed)
	first, err := h.service.Merge(ctx, MergeCommand{
		Dictionary: testDict,
		ID:         c.Entry.ID,
		Into:       a.Entry.ID,
		IfMatch:    c.Entry.Revision,
		Meta:       operatorMeta,
	})
	if err != nil || len(first.Entry.Aliases) != 3 {
		t.Fatalf("объединение c → a: %+v %v", first, err)
	}
	current, _ := h.store.ReferenceEntry(a.Entry.ID)
	merged, err := h.service.Merge(ctx, MergeCommand{
		Dictionary: testDict,
		ID:         a.Entry.ID,
		Into:       b.Entry.ID,
		IfMatch:    current.Revision,
		Meta:       operatorMeta,
	})
	if err != nil || merged.Entry.ID != b.Entry.ID || len(merged.Entry.Aliases) != 4 {
		t.Fatalf("объединение a → b: %+v %v", merged, err)
	}
	chained, _ := h.store.ReferenceEntry(c.Entry.ID)
	if chained.MergedInto != b.Entry.ID {
		t.Fatalf("цепочка не схлопнулась: %+v", chained)
	}
	view, err := h.service.Get(ctx, testDict, c.Entry.ID)
	if err != nil || view.Resolved == nil || view.Resolved.ID != b.Entry.ID {
		t.Fatalf("объединённая запись не разрешается: %+v %v", view, err)
	}
	resolved, err := h.service.Resolve(ctx, testDict, "brend")
	if err != nil || resolved.Match == nil || resolved.Match.EntryID != b.Entry.ID {
		t.Fatalf("вариант не перешёл к выжившей: %+v %v", resolved, err)
	}
	last := h.events(t)[len(h.events(t))-1]
	if !reflect.DeepEqual(last.Kinds, []reference.EventKind{
		reference.EventAliasAdded,
		reference.EventAliasRemoved,
		reference.EventMerged,
	}) || len(last.EntryIDs) != 3 {
		t.Fatalf("событие объединения: %+v", last)
	}
}

func TestAliasRepeatAndRemoval(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	entry := h.create(t, "Бренд", reference.StatusConfirmed)
	added, err := h.service.AddAlias(ctx, AddAliasCommand{
		Dictionary: testDict,
		EntryID:    entry.Entry.ID,
		Alias:      AliasSpec{Text: "Brend", Kind: reference.AliasTranslit},
		Meta:       operatorMeta,
	})
	if err != nil || !added.Changed || added.Alias.Key != "brend" || added.Revision != 2 {
		t.Fatalf("вариант: %+v %v", added, err)
	}
	repeat, err := h.service.AddAlias(ctx, AddAliasCommand{
		Dictionary: testDict,
		EntryID:    entry.Entry.ID,
		Alias:      AliasSpec{Text: "BREND"},
		Meta:       operatorMeta,
	})
	if err != nil || repeat.Changed || repeat.Revision != 2 {
		t.Fatalf("повтор варианта: %+v %v", repeat, err)
	}
	removed, err := h.service.RemoveAlias(ctx, RemoveAliasCommand{
		Dictionary: testDict,
		AliasID:    added.Alias.ID,
		Meta:       operatorMeta,
	})
	if err != nil || !removed.Changed || len(removed.Entry.Aliases) != 1 {
		t.Fatalf("удаление: %+v %v", removed, err)
	}
	canonical := removed.Entry.Aliases[0].ID
	removal := RemoveAliasCommand{Dictionary: testDict, AliasID: canonical, Meta: operatorMeta}
	_, err = h.service.RemoveAlias(ctx, removal)
	if !errors.Is(err, reference.ErrCanonicalAlias) {
		t.Fatalf("удаление канонического варианта: %v", err)
	}
	if kinds := h.events(t)[2].Kinds; !reflect.DeepEqual(kinds, []reference.EventKind{reference.EventAliasRemoved}) {
		t.Fatalf("событие удаления: %v", kinds)
	}
}

func TestEntryLimitAndNormalizer(t *testing.T) {
	h := newHarness(t)
	limits := reference.DefaultLimits()
	limits.MaxEntries = 1
	h.store.SeedReferenceDictionary(reference.DictionaryState{
		Code:       testDict,
		AliasKinds: reference.AliasKinds,
		Limits:     limits,
	})
	h.create(t, "Бренд", reference.StatusConfirmed)
	_, err := h.service.Create(context.Background(), CreateCommand{
		Dictionary: testDict,
		Name:       "Бренд Два",
		Status:     reference.StatusConfirmed,
		Meta:       operatorMeta,
	})
	if !errors.Is(err, reference.ErrLimit) {
		t.Fatalf("лимит записей: %v", err)
	}
	limits.Normalizer = "v0"
	other := reference.DictionaryState{Code: "b2", AliasKinds: reference.AliasKinds, Limits: limits}
	h.store.SeedReferenceDictionary(other)
	_, err = h.service.Create(context.Background(), CreateCommand{
		Dictionary: "b2",
		Name:       "Бренд",
		Status:     reference.StatusConfirmed,
		Meta:       operatorMeta,
	})
	if !errors.Is(err, reference.ErrNormalizer) {
		t.Fatalf("другая версия нормализации: %v", err)
	}
	_, err = h.service.Resolve(context.Background(), "b3", "Бренд")
	if !errors.Is(err, reference.ErrDictionaryNotFound) {
		t.Fatalf("неизвестный словарь: %v", err)
	}
}
