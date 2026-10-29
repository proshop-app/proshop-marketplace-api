package reference

import (
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestDraftRecordsOnlyChangedEntries(t *testing.T) {
	dict := testDictionary()
	brend := AliasInput{ID: "a2", Text: "Brend"}
	source := RestoreEntry(savedState(t, newTestEntry(t, "Бренд", StatusConfirmed, brend)))
	target := RestoreEntry(savedState(t, newEntryWithID(t, "e2", "b2", "Бренд Два", StatusConfirmed)))
	idle := RestoreEntry(savedState(t, newEntryWithID(t, "e3", "b3", "Третий", StatusConfirmed)))

	draft := NewDraft("b1")
	draft.Track(source)
	draft.Track(target)
	draft.Track(idle)
	if _, err := source.MergeInto(dict, target, testNow); err != nil {
		t.Fatal(err)
	}
	change, ok := draft.Change(ChangeMerged, source.ID(), Meta{Actor: "t1"}, testNow)
	if !ok || len(change.Forward) != 2 || len(draft.Saved()) != 2 {
		t.Fatalf("изменение: %+v", change)
	}
	if change.Inverse[0].State.Status != StatusConfirmed || change.Forward[0].State.Status != StatusMerged {
		t.Fatal("образы до и после перепутаны")
	}
	if len(draft.ClaimedKeys()) != 2 || !draft.Releases("brend", source.ID()) {
		t.Fatalf("ключи переносятся внутри изменения: %v", draft.ClaimedKeys())
	}
	effects := change.Effects()
	want := []EventKind{EventAliasAdded, EventAliasRemoved, EventMerged}
	if !reflect.DeepEqual(effects.Kinds, want) || !reflect.DeepEqual(effects.Keys, []string{"brend", "бренд"}) {
		t.Fatalf("следствия: %+v", effects)
	}
}

func TestDraftWithoutChangesWritesNothing(t *testing.T) {
	e := RestoreEntry(savedState(t, newTestEntry(t, "Бренд", StatusConfirmed)))
	draft := NewDraft("b1")
	draft.Track(e)
	if _, err := e.ChangeStatus(StatusConfirmed, testNow); err != nil {
		t.Fatal(err)
	}
	if _, ok := draft.Change(ChangeUpdated, e.ID(), Meta{}, testNow); ok {
		t.Fatal("повтор записал ревизию")
	}
}

func TestDraftCreateAndDeleteDelta(t *testing.T) {
	created := newTestEntry(t, "Бренд", StatusCandidate)
	draft := NewDraft("b1")
	draft.Track(created)
	change, ok := draft.Change(ChangeCreated, created.ID(), Meta{}, testNow)
	if !ok || draft.EntriesDelta() != 1 || change.Inverse[0].State != nil {
		t.Fatalf("создание: %+v", change)
	}
	if kinds := change.Effects().Kinds; !reflect.DeepEqual(kinds, []EventKind{EventAliasAdded}) {
		t.Fatalf("виды создания: %v", kinds)
	}

	stored := RestoreEntry(savedState(t, created))
	removal := NewDraft("b1")
	removal.Delete(stored)
	change, ok = removal.Change(ChangeReverted, stored.ID(), Meta{}, testNow)
	if !ok || removal.EntriesDelta() != -1 || len(removal.Deleted()) != 1 || change.Forward[0].State != nil {
		t.Fatalf("удаление: %+v", change)
	}
}

func TestEffectsJoinDeduplicates(t *testing.T) {
	a := Effects{Kinds: []EventKind{EventStatusChanged}, Entries: []ids.UUID{"e1"}, Keys: []string{"k2"}}
	b := Effects{
		Kinds:   []EventKind{EventAliasAdded, EventStatusChanged},
		Entries: []ids.UUID{"e1", "e2"},
		Keys:    []string{"k1"},
	}
	joined := a.Join(b)
	if !reflect.DeepEqual(joined.Kinds, []EventKind{EventAliasAdded, EventStatusChanged}) ||
		len(joined.Entries) != 2 || !reflect.DeepEqual(joined.Keys, []string{"k1", "k2"}) {
		t.Fatalf("объединение: %+v", joined)
	}
}

func savedState(t *testing.T, e *Entry) EntryState {
	t.Helper()
	e.Touch(1, testNow)
	return e.State()
}
