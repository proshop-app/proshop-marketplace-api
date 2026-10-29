package reference

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

var testNow = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

func TestNewEntryKeepsCanonicalAsAlias(t *testing.T) {
	e := newTestEntry(t, "Бренд", StatusConfirmed, AliasInput{ID: "a2", Text: "Brend", Kind: AliasTranslit})
	state := e.State()
	if state.CanonicalKey != "бренд" || len(state.Aliases) != 2 {
		t.Fatalf("запись: %+v", state)
	}
	if _, ok := e.Alias("бренд"); !ok {
		t.Fatal("каноническое имя не стало вариантом")
	}
	if string(state.Metadata) != "{}" {
		t.Fatalf("metadata по умолчанию: %s", state.Metadata)
	}
}

func TestNewEntryRejectsFinalStatuses(t *testing.T) {
	_, err := NewEntry(testDictionary(), NewEntryInput{ID: "e1", Name: "Бренд", Status: StatusMerged}, testNow)
	if !errors.Is(err, ErrTransition) {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusTransitions(t *testing.T) {
	e := newTestEntry(t, "Бренд", StatusCandidate)
	if changed, err := e.ChangeStatus(StatusConfirmed, testNow); !changed || err != nil {
		t.Fatalf("candidate → confirmed: %v %v", changed, err)
	}
	if changed, err := e.ChangeStatus(StatusConfirmed, testNow); changed || err != nil {
		t.Fatalf("повтор статуса — не изменение: %v %v", changed, err)
	}
	if _, err := e.ChangeStatus(StatusRejected, testNow); !errors.Is(err, ErrTransition) {
		t.Fatalf("confirmed → rejected: %v", err)
	}
	if changed, err := e.ChangeStatus(StatusDeprecated, testNow); !changed || err != nil {
		t.Fatalf("confirmed → deprecated: %v %v", changed, err)
	}
	other := AliasInput{ID: "a9", Text: "Другое"}
	if _, _, err := e.AddAlias(testDictionary(), other, SourceOperator, testNow); err == nil {
		t.Fatal("выведенная запись приняла вариант")
	}
}

func TestAliasRepeatLimitAndCanonical(t *testing.T) {
	dict := RestoreDictionary(DictionaryState{
		Code:       "b1",
		AliasKinds: []AliasKind{AliasSpelling},
		Limits: Limits{
			MaxEntries:     10,
			MaxAliases:     2,
			MaxAliasLength: 120,
			MaxAliasWords:  8,
			Normalizer:     NormalizerVersion,
		},
	})
	input := NewEntryInput{ID: "e1", CanonicalAliasID: "a1", Name: "Бренд", Status: StatusConfirmed}
	e, err := NewEntry(dict, input, testNow)
	if err != nil {
		t.Fatal(err)
	}
	alias, added, err := e.AddAlias(dict, AliasInput{ID: "a2", Text: "БРЕНД  2"}, SourceOperator, testNow)
	if err != nil || !added || alias.Key != "бренд 2" {
		t.Fatalf("AddAlias: %+v %v %v", alias, added, err)
	}
	repeat := AliasInput{ID: "a3", Text: "бренд 2"}
	if _, added, err := e.AddAlias(dict, repeat, SourceOperator, testNow); added || err != nil {
		t.Fatalf("повтор ключа — не изменение: %v %v", added, err)
	}
	third := AliasInput{ID: "a4", Text: "Третий"}
	if _, _, err := e.AddAlias(dict, third, SourceOperator, testNow); !errors.Is(err, ErrLimit) {
		t.Fatalf("лимит вариантов: %v", err)
	}
	translit := AliasInput{ID: "a5", Text: "x", Kind: AliasTranslit}
	if _, _, err := e.AddAlias(dict, translit, SourceOperator, testNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("вид не из словаря: %v", err)
	}
	if _, err := e.RemoveAlias("a1", testNow); !errors.Is(err, ErrCanonicalAlias) {
		t.Fatalf("удаление канонического варианта: %v", err)
	}
	if _, err := e.RemoveAlias("a2", testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RemoveAlias("a2", testNow); !errors.Is(err, ErrAliasNotFound) {
		t.Fatalf("повторное удаление: %v", err)
	}
}

func TestRenameKeepsFormerSpelling(t *testing.T) {
	dict := testDictionary()
	e := newTestEntry(t, "Бренд", StatusConfirmed)
	changed, err := e.Rename(dict, "Бренд Два", "a2", testNow)
	if err != nil || !changed {
		t.Fatalf("Rename: %v %v", changed, err)
	}
	state := e.State()
	if state.CanonicalKey != "бренд два" || len(state.Aliases) != 2 {
		t.Fatalf("после переименования: %+v", state)
	}
	if changed, _ := e.Rename(dict, "Бренд Два", "a3", testNow); changed {
		t.Fatal("повтор имени — изменение")
	}
	// смена регистра: ключ тот же, вариант не добавляется
	if changed, _ := e.Rename(dict, "БРЕНД ДВА", "a4", testNow); !changed || len(e.State().Aliases) != 2 {
		t.Fatalf("регистр: %+v", e.State())
	}
}

func TestMergeMovesAliasesAndRepoints(t *testing.T) {
	dict := testDictionary()
	source := newTestEntry(t, "Бренд", StatusConfirmed, AliasInput{ID: "a2", Text: "Brend"})
	target := newEntryWithID(t, "e2", "b2", "Бренд Два", StatusConfirmed)
	changed, err := source.MergeInto(dict, target, testNow)
	if err != nil || !changed {
		t.Fatalf("MergeInto: %v %v", changed, err)
	}
	if source.Status() != StatusMerged || source.MergedInto() != "e2" || len(source.State().Aliases) != 0 {
		t.Fatalf("источник: %+v", source.State())
	}
	if len(target.State().Aliases) != 3 {
		t.Fatalf("выживший: %+v", target.State())
	}
	if changed, err := source.MergeInto(dict, target, testNow); changed || err != nil {
		t.Fatalf("повтор объединения: %v %v", changed, err)
	}
	if !source.Repoint("e3") || source.MergedInto() != "e3" {
		t.Fatal("цепочка не схлопнулась")
	}
	candidate := newEntryWithID(t, "e4", "b4", "Кандидат", StatusCandidate)
	if _, err := candidate.MergeInto(dict, target, testNow); !errors.Is(err, ErrMergeTarget) {
		t.Fatalf("кандидат объединился: %v", err)
	}
}

func TestMetadataDirectionsAreStrings(t *testing.T) {
	if _, err := CleanMetadata(json.RawMessage(`{"directions":["d1","d2"]}`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`[]`, `{"directions":"d1"}`, `{"directions":[1]}`, `null`} {
		if _, err := CleanMetadata(json.RawMessage(raw)); err == nil {
			t.Fatalf("metadata %s прошла", raw)
		}
	}
}

func TestSameContentIgnoresVolatileFields(t *testing.T) {
	e := newTestEntry(t, "Бренд", StatusConfirmed)
	a := e.State()
	e.Touch(7, testNow.Add(time.Hour))
	e.Meet(testNow)
	b := e.State()
	if !SameContent(&a, &b) {
		t.Fatal("версия и ревизия повлияли на содержимое")
	}
	b.Status = StatusDeprecated
	if SameContent(&a, &b) {
		t.Fatal("статус не повлиял на содержимое")
	}
	if !SameContent(nil, nil) || SameContent(&a, nil) {
		t.Fatal("сравнение отсутствующих записей")
	}
}

func newTestEntry(t *testing.T, name string, status Status, aliases ...AliasInput) *Entry {
	t.Helper()
	return newEntryWithID(t, "e1", "a1", name, status, aliases...)
}

func newEntryWithID(t *testing.T, id, alias ids.UUID, name string, status Status, aliases ...AliasInput) *Entry {
	t.Helper()
	input := NewEntryInput{
		ID:               id,
		CanonicalAliasID: alias,
		Name:             name,
		Status:           status,
		Source:           SourceOperator,
		Aliases:          aliases,
	}
	e, err := NewEntry(testDictionary(), input, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
