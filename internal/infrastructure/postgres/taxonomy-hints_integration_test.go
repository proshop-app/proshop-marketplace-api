//go:build integration

package postgres_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
)

const (
	conflictHintHistory       = `INSERT INTO taxonomy_versions (version, definition) VALUES (4, '{}')`
	removeHintHistoryConflict = `DELETE FROM taxonomy_versions WHERE version = 4`
)

func TestScopedHintsAtomicHistoryAndLegacyCorrection(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	uow := postgres.NewUnitOfWork(pool)
	registry := appreference.NewService(uow, clock.System{}, random.IDs{}, nil, appreference.Config{})
	_, err := registry.Import(ctx, "", reference.ImportPackage{
		SchemaVersion: 1,
		Sources:       []reference.ImportSource{{ID: "s1", URL: "https://example.org/data", License: "CC0-1.0"}},
		Dictionaries: []reference.ImportDictionary{
			{
				Code:    "d1",
				Kind:    reference.KindOpen,
				Release: 1,
				Entries: []reference.DeliveryEntry{{Canonical: "Первое"}},
			},
			{
				Code:    "d2",
				Kind:    reference.KindOpen,
				Release: 1,
				Entries: []reference.DeliveryEntry{{Canonical: "Второе"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service := apptaxonomy.NewService(uow, clock.System{}, nil)
	d := testTaxonomy(1)
	d.Attributes[0].Type = "string"
	release := taxonomy.Release{
		Version:    1,
		Checksum:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Attributes: d.Attributes,
		Schemas:    d.Schemas,
		Categories: d.Categories,
	}
	if _, err := service.Apply(ctx, release); err != nil {
		t.Fatal(err)
	}
	d.Attributes[0].Dictionary = "d1"
	if _, err := service.LoadPart(ctx, "attributes", d, 1); err != nil {
		t.Fatal(err)
	}
	rule := taxonomy.DictionaryBinding{
		CategoryID:    "c1",
		AttributeCode: "a1",
		Mode:          "extend",
		Dictionaries:  []string{"d2"},
	}
	if version, err := service.PutDictionaryBinding(ctx, rule, 2); err != nil || version != 3 {
		t.Fatal(version, err)
	}
	before, _ := service.Current(ctx)
	if _, err := pool.Exec(ctx, conflictHintHistory); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteDictionaryBinding(ctx, "c1", "a1", 3); err == nil {
		t.Fatal("конфликт истории не откатил head update")
	}
	after, _ := service.Current(ctx)
	if !reflect.DeepEqual(after, before) {
		t.Fatal("частичный rules update пережил отказ истории", after, before)
	}
	if _, err := pool.Exec(ctx, removeHintHistoryConflict); err != nil {
		t.Fatal(err)
	}
	version, err := service.RemoveHint(ctx, apptaxonomy.RemoveHintCommand{
		Attribute:       "a1",
		Dictionary:      "d1",
		PreviousVersion: 1,
		ExpectedVersion: 3,
	})
	if err != nil || version != 4 {
		t.Fatal(version, err)
	}
	current, _ := service.Current(ctx)
	rulesPreserved := reflect.DeepEqual(current.DictionaryBindings, before.DictionaryBindings)
	if current.Attributes[0].Dictionary != "" || !rulesPreserved {
		t.Fatal("коррекция legacy изменила scoped rules", current)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		original, err := tx.Taxonomy().Version(ctx, 1)
		if err != nil || original.Attributes[0].Dictionary != "" {
			t.Fatal("историческое доказательство изменилось", original, err)
		}
		prior, err := tx.Taxonomy().Version(ctx, 3)
		if err != nil || prior.Attributes[0].Dictionary != "d1" {
			t.Fatal("прежний head не сохранился", prior, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
