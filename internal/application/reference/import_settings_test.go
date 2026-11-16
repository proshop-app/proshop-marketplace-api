package reference

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

type dictionarySaveCounter struct {
	ports.UnitOfWork
	saves int
}

type dictionarySaveTx struct {
	ports.Tx
	counter *dictionarySaveCounter
}

type dictionarySaveRepository struct {
	reference.Repository
	counter *dictionarySaveCounter
}

func (c *dictionarySaveCounter) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return c.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return fn(ctx, dictionarySaveTx{Tx: tx, counter: c})
	})
}

func (tx dictionarySaveTx) Reference() reference.Repository {
	return dictionarySaveRepository{Repository: tx.Tx.Reference(), counter: tx.counter}
}

func (r dictionarySaveRepository) SaveDictionary(ctx context.Context, dictionary *reference.Dictionary) error {
	r.counter.saves++
	return r.Repository.SaveDictionary(ctx, dictionary)
}

func TestImportNewReleaseWithSameSettingsDoesNotSaveHeader(t *testing.T) {
	h := newHarness(t)
	counter := &dictionarySaveCounter{UnitOfWork: h.store}
	h.service.uow = counter
	input := importPackageFixture()
	input.Dictionaries = input.Dictionaries[:1]
	input.Dictionaries[0].Entries = []reference.DeliveryEntry{}
	if _, err := h.service.Import(context.Background(), "", input); err != nil {
		t.Fatal(err)
	}
	counter.saves = 0
	input.Dictionaries[0].Release++
	result, err := h.service.Import(context.Background(), "", input)
	if err != nil || !result[0].Changed || counter.saves != 0 {
		t.Fatal("выпуск должен записаться без сохранения заголовка", result, counter.saves, err)
	}
}

func importedSettings(t *testing.T, h *harness, code string) reference.DictionaryState {
	t.Helper()
	var state reference.DictionaryState
	err := h.store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		dictionary, err := tx.Reference().Dictionary(ctx, code)
		if err == nil {
			state = dictionary.State()
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestImportSettingsCreateChangeAndReplay(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	input := importPackageFixture()
	input.Dictionaries = input.Dictionaries[:1]
	dictionary := &input.Dictionaries[0]
	dictionary.Identity = true
	dictionary.AliasKinds = []reference.AliasKind{reference.AliasSpelling}
	dictionary.Limits.MaxAliases = 2
	dictionary.Limits.MaxEntries = 10
	first, err := h.service.Import(ctx, "", input)
	if err != nil || !first[0].Created {
		t.Fatal(first, err)
	}
	state := importedSettings(t, h, dictionary.Code)
	if !state.Identity || state.Limits != dictionary.Limits ||
		!reflect.DeepEqual(state.AliasKinds, dictionary.AliasKinds) {
		t.Fatal("настройки создания потеряны", state)
	}
	dictionary.Limits.MaxAliases = 3
	if _, err := h.service.Import(ctx, "", input); !errors.Is(err, reference.ErrDeliveryConflict) {
		t.Fatal("подмена заголовка при том же выпуске принята", err)
	}
	if got := importedSettings(t, h, dictionary.Code); !reflect.DeepEqual(got, state) {
		t.Fatal("конфликт оставил изменения", got)
	}
	dictionary.Release++
	dictionary.AliasKinds = append(dictionary.AliasKinds, reference.AliasLocale)
	dictionary.Limits.MaxEntries = 1
	changed, err := h.service.Import(ctx, "", input)
	if err != nil || !changed[0].Changed || changed[0].Created || changed[0].CreatedEntries != 0 {
		t.Fatal(changed, err)
	}
	updated := importedSettings(t, h, dictionary.Code)
	if updated.Limits != dictionary.Limits || len(updated.AliasKinds) != 2 {
		t.Fatal("безопасное изменение не сохранено", updated)
	}
	events := len(h.events(t))
	repeated, err := h.service.Import(ctx, "", input)
	if err != nil || repeated[0].Changed || len(h.events(t)) != events {
		t.Fatal("повтор изменил словарь", repeated, err)
	}
	if got := importedSettings(t, h, dictionary.Code); !reflect.DeepEqual(got, updated) {
		t.Fatal(got)
	}
}

func TestImportSettingsRejectUnsafeChangesAtomically(t *testing.T) {
	cases := map[string]func(*reference.ImportDictionary){
		"kind": func(d *reference.ImportDictionary) {
			d.Kind = reference.KindOpen
		},
		"identity": func(d *reference.ImportDictionary) {
			d.Identity = !d.Identity
		},
		"maxAliases": func(d *reference.ImportDictionary) {
			d.Limits.MaxAliases--
		},
		"maxAliasLength": func(d *reference.ImportDictionary) {
			d.Limits.MaxAliasLength--
		},
		"maxAliasWords": func(d *reference.ImportDictionary) {
			d.Limits.MaxAliasWords--
		},
		"aliasKinds": func(d *reference.ImportDictionary) {
			d.AliasKinds = []reference.AliasKind{reference.AliasSpelling}
		},
		"maxEntries": func(d *reference.ImportDictionary) {
			d.Limits.MaxEntries = 1
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			input := importPackageFixture()
			input.Dictionaries[1].Entries = []reference.DeliveryEntry{
				{Canonical: "Первое"},
				{Canonical: "Второе"},
			}
			if _, err := h.service.Import(ctx, "", input); err != nil {
				t.Fatal(err)
			}
			before := importedSettings(t, h, "a1")
			input.Dictionaries[0].Release++
			input.Dictionaries[0].Limits.MaxEntries--
			input.Dictionaries[1].Release++
			change(&input.Dictionaries[1])
			if _, err := h.service.Import(ctx, "", input); !errors.Is(err, reference.ErrSettingsConflict) {
				t.Fatal("опасные настройки приняты", err)
			}
			if after := importedSettings(t, h, "a1"); !reflect.DeepEqual(before, after) {
				t.Fatal("ранний словарь не откатился", after)
			}

		})
	}
}

func TestImportEmptyDictionaryCanChangeKindAndIdentity(t *testing.T) {
	h := newHarness(t)
	input := importPackageFixture()
	input.Dictionaries = input.Dictionaries[:1]
	input.Dictionaries[0].Entries = []reference.DeliveryEntry{}
	if _, err := h.service.Import(context.Background(), "", input); err != nil {
		t.Fatal(err)
	}
	dictionary := &input.Dictionaries[0]
	dictionary.Release++
	dictionary.Kind = reference.KindClosed
	dictionary.Identity = true
	dictionary.Entries = []reference.DeliveryEntry{{Canonical: "Значение"}}
	if _, err := h.service.Import(context.Background(), "", input); err != nil {
		t.Fatal(err)
	}
	state := importedSettings(t, h, dictionary.Code)
	if state.Kind != reference.KindClosed || !state.Identity || state.Entries != 1 {
		t.Fatal(state)
	}
}
