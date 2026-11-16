//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
)

func referenceImportFixture() reference.ImportPackage {
	return reference.ImportPackage{
		SchemaVersion: 2,
		Sources: []reference.ImportSource{
			{ID: "s1", URL: "https://example.org/data", License: "CC0-1.0"},
		},
		Dictionaries: []reference.ImportDictionary{
			{
				Code:       "a1",
				Kind:       reference.KindOpen,
				AliasKinds: reference.AliasKinds,
				Limits:     reference.DefaultLimits(),
				Release:    1,
				Entries:    []reference.DeliveryEntry{{Canonical: "Первое"}},
			},
			{
				Code:       "c1",
				Kind:       reference.KindClosed,
				AliasKinds: reference.AliasKinds,
				Limits:     reference.DefaultLimits(),
				Release:    1,
				Entries:    []reference.DeliveryEntry{{Canonical: "Второе"}},
			},
		},
	}
}

func TestReferenceImportCreatesAndReplaysMixedDictionaries(t *testing.T) {
	pool := testPool(t)
	clock := &referenceClock{now: time.Now().UTC()}
	service := appreference.NewService(postgres.NewUnitOfWork(pool), clock, random.IDs{}, nil, appreference.Config{})
	input := referenceImportFixture()
	results, err := service.Import(context.Background(), "", input)
	if err != nil || len(results) != 2 || !results[0].Created || !results[1].Created {
		t.Fatal(results, err)
	}
	snapshot, err := service.Snapshot(context.Background(), "c1", nil)
	if err != nil || snapshot.Kind != reference.KindClosed || len(snapshot.Entries) != 1 ||
		snapshot.Entries[0].Status != reference.StatusCandidate {
		t.Fatal(snapshot, err)
	}
	again, err := service.Import(context.Background(), "", input)
	if err != nil || again[0].Changed || again[1].Changed || again[0].Revision != results[0].Revision {
		t.Fatal(again, err)
	}
}

func TestReferenceImportFailureRollsBackNewDictionaryAndJournal(t *testing.T) {
	pool := testPool(t)
	clock := &referenceClock{now: time.Now().UTC()}
	service := appreference.NewService(postgres.NewUnitOfWork(pool), clock, random.IDs{}, nil, appreference.Config{})
	input := referenceImportFixture()
	input.Dictionaries = input.Dictionaries[1:]
	if _, err := service.Import(context.Background(), "", input); err != nil {
		t.Fatal(err)
	}
	before, err := service.Snapshot(context.Background(), "c1", nil)
	if err != nil {
		t.Fatal(err)
	}
	input = referenceImportFixture()
	input.Dictionaries[1].Release = 2
	input.Dictionaries[1].Entries = []reference.DeliveryEntry{
		{Canonical: "Третье", Aliases: []reference.DeliveryAlias{{Text: "Второе"}}},
	}
	if _, err := service.Import(context.Background(), "", input); !errors.Is(err, reference.ErrKeyTaken) {
		t.Fatal(err)
	}
	if _, err := service.Snapshot(context.Background(), "a1", nil); !errors.Is(err, reference.ErrDictionaryNotFound) {
		t.Fatal("новый словарь остался после отката", err)
	}
	after, err := service.Snapshot(context.Background(), "c1", nil)
	if err != nil || after.Revision != before.Revision || len(after.Entries) != 1 {
		t.Fatal("журнал изменился после отката", after, err)
	}
}

func importReferenceEntries(
	ctx context.Context,
	service *appreference.Service,
	entries []reference.DeliveryEntry,
) (appreference.ImportResult, error) {
	input := referenceImportFixture()
	input.Dictionaries = []reference.ImportDictionary{
		{
			Code:       referenceDict,
			Kind:       reference.KindOpen,
			Identity:   true,
			AliasKinds: reference.AliasKinds,
			Limits:     reference.DefaultLimits(),
			Release:    1,
			Entries:    entries,
		},
	}
	results, err := service.Import(ctx, "", input)
	if err != nil {
		return appreference.ImportResult{}, err
	}
	return results[0], nil
}

func TestReferenceImportPersistsSettingsAndRollsBackUnsafeChange(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	service := appreference.NewService(
		postgres.NewUnitOfWork(pool),
		&referenceClock{now: time.Now().UTC()},
		random.IDs{},
		nil,
		appreference.Config{},
	)
	input := referenceImportFixture()
	input.Dictionaries = input.Dictionaries[:1]
	dictionary := &input.Dictionaries[0]
	dictionary.Identity = true
	dictionary.Limits.MaxAliases = 2
	if _, err := service.Import(ctx, "", input); err != nil {
		t.Fatal(err)
	}
	dictionary.Release++
	dictionary.Limits.MaxAliases = 3
	if _, err := service.Import(ctx, "", input); err != nil {
		t.Fatal(err)
	}
	err := postgres.NewUnitOfWork(pool).Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		state, err := tx.Reference().Dictionary(ctx, dictionary.Code)
		if err != nil {
			return err
		}
		if !state.State().Identity || state.Limits().MaxAliases != 3 {
			t.Fatal(state.State())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	dictionary.Release++
	dictionary.Identity = false
	if _, err := service.Import(ctx, "", input); !errors.Is(err, reference.ErrSettingsConflict) {
		t.Fatal("опасное изменение принято", err)
	}
	dictionary.Identity = true
	if results, err := service.Import(ctx, "", input); err != nil || !results[0].Changed {
		t.Fatal("отказ занял номер выпуска", results, err)
	}
}
