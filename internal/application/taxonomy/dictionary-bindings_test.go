package taxonomy_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func stringDefinition(version int64, dictionary string) taxonomy.Definition {
	input := schema(version)
	input.Attributes[0].Type = "string"
	input.Attributes[0].Values = nil
	input.Attributes[0].Dictionary = dictionary
	return input
}

func bindingRelease() taxonomy.Release {
	next := stringDefinition(2, "a1")
	return taxonomy.Release{
		Version:         7,
		Checksum:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Attributes:      next.Attributes,
		Schemas:         next.Schemas,
		Categories:      next.Categories,
		ExpectedVersion: 1,
	}
}

func TestHintImportBindingReviewAndRollbackPreserveFreeStrings(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	original := stringDefinition(1, "")
	store.SeedTaxonomy(original)
	clock := fake.NewClock(time.Now())
	service := apptaxonomy.NewService(store, clock, nil)
	registry := appreference.NewService(store, clock, &fake.IDs{}, &fake.Publisher{}, appreference.Config{})
	release := bindingRelease()
	if _, err := service.Apply(ctx, release); !errors.Is(err, taxonomy.ErrInvalid) {
		t.Fatal("таксономия не требует предварительного импорта словаря", err)
	}
	before, _ := service.Current(ctx)
	if !reflect.DeepEqual(before, original) || len(store.OutboxRows()) != 0 {
		t.Fatal("неудачная публикация изменила снимок или события")
	}
	_, err := registry.Import(ctx, "", reference.ImportPackage{
		SchemaVersion: 1,
		Sources:       []reference.ImportSource{{ID: "s1", URL: "https://example.org/data", License: "CC0-1.0"}},
		Dictionaries: []reference.ImportDictionary{
			{
				Code:    "a1",
				Kind:    reference.KindOpen,
				Release: 2,
				Entries: []reference.DeliveryEntry{{Canonical: "Подсказка"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	projector := appcatalog.NewProjector(store, clock, nil)
	raw := json.RawMessage(`"  Неизвестная прежняя строка  "`)
	if err := projector.ApplyProduct(ctx, appcatalog.ProductEvent{
		EventID: "e1",
		Snapshot: catalog.Snapshot{
			TenantID:  "t1",
			ProductID: "p1",
			Revision:  1,
			At:        clock.Now(),
			Card: catalog.Card{
				Name:    "Товар",
				Visible: true,
				Marketplace: &taxonomy.Card{
					CategoryID: "c1",
					Published:  true,
					Attributes: map[string]json.RawMessage{"a1": raw},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(ctx, release)
	if err != nil || !result.Changed || result.SnapshotVersion != 2 {
		t.Fatal(result, err)
	}
	current, _ := service.Current(ctx)
	product, _ := store.Product("t1", "p1")
	if current.Attributes[0].Dictionary != "a1" || len(current.Attributes[0].Values) != 0 ||
		!product.Card.Publication.Published || string(product.Card.Publication.Attributes["a1"]) != string(raw) {
		t.Fatal("подсказки ограничили или переписали старую строку", current, product.Card.Publication)
	}
	snapshot, err := registry.Snapshot(ctx, "a1", nil)
	if err != nil || len(snapshot.Entries) != 1 || snapshot.Entries[0].Status != reference.StatusCandidate {
		t.Fatal("импорт автоматически подтвердил кандидата", snapshot, err)
	}
	confirmed := reference.StatusConfirmed
	_, err = registry.Update(ctx, appreference.UpdateCommand{
		Dictionary: "a1",
		ID:         snapshot.Entries[0].ID,
		IfMatch:    snapshot.Entries[0].Revision,
		Status:     &confirmed,
		Meta:       reference.Meta{Actor: "o1", Source: reference.SourceOperator},
	})
	if err != nil {
		t.Fatal(err)
	}
	delta, err := registry.Snapshot(ctx, "a1", &snapshot.Revision)
	if err != nil || delta.Full || len(delta.Entries) != 1 || delta.Entries[0].Status != confirmed {
		t.Fatal("потребитель не получит подтверждение через дельту", delta, err)
	}
	current, _ = service.Current(ctx)
	if current.Version != 2 {
		t.Fatal("подтверждение подсказки требует публикации таксономии", current.Version)
	}
	again, err := service.Apply(ctx, release)
	if err != nil || again.Changed || again.SnapshotVersion != 2 {
		t.Fatal("повтор поставки изменил версию", again, err)
	}
	if _, err := service.LoadPart(ctx, "attributes", original, 2); !errors.Is(err, taxonomy.ErrConflict) {
		t.Fatal("обычная загрузка удалила привязку", err)
	}
	rolledBack, err := service.Rollback(ctx, release)
	if err != nil || !rolledBack.Changed || rolledBack.SnapshotVersion != 3 {
		t.Fatal("последняя additive-поставка не откатывается", rolledBack, err)
	}
	restored, _ := service.Current(ctx)
	original.Version = 3
	if !reflect.DeepEqual(restored, original) {
		t.Fatal("откат не восстановил исходный снимок", restored)
	}
	product, _ = store.Product("t1", "p1")
	entry, err := registry.Get(ctx, "a1", snapshot.Entries[0].ID)
	if err != nil || entry.Entry.Status != confirmed || !product.Card.Publication.Published ||
		string(product.Card.Publication.Attributes["a1"]) != string(raw) {
		t.Fatal("откат изменил реестр или товар", entry, product.Card.Publication, err)
	}
	rolledBack, err = service.Rollback(ctx, release)
	if err != nil || rolledBack.Changed || rolledBack.SnapshotVersion != 3 {
		t.Fatal("повтор отката изменил версию", rolledBack, err)
	}
	if _, err := service.Apply(ctx, release); !errors.Is(err, taxonomy.ErrReleaseConflict) {
		t.Fatal("отменённая поставка применена повторно", err)
	}
}

func TestStringHintBindingRejectsClosedDictionaryAndProtectsOperatorEdit(t *testing.T) {
	for _, operation := range []string{"part", "release"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			store := fake.NewStore()
			original := stringDefinition(1, "")
			store.SeedTaxonomy(original)
			seedValues(t, store, "v1")
			service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil)
			var err error
			if operation == "part" {
				_, err = service.LoadPart(ctx, "attributes", stringDefinition(2, "a1"), 1)
			} else {
				_, err = service.Apply(ctx, bindingRelease())
			}
			if !errors.Is(err, taxonomy.ErrInvalid) {
				t.Fatal("закрытый словарь принят строковым атрибутом", err)
			}
			current, _ := service.Current(ctx)
			if !reflect.DeepEqual(current, original) || len(store.OutboxRows()) != 0 {
				t.Fatal("ошибка оставила частичную публикацию", current)
			}
		})
	}
	ctx := context.Background()
	store := fake.NewStore()
	store.SeedTaxonomy(stringDefinition(1, ""))
	store.SeedReferenceDictionary(reference.DictionaryState{Code: "a1", Kind: reference.KindOpen})
	service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil)
	release := bindingRelease()
	if _, err := service.Apply(ctx, release); err != nil {
		t.Fatal(err)
	}
	current, _ := service.Current(ctx)
	if _, err := service.LoadPart(ctx, "schemas", current, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rollback(ctx, release); !errors.Is(err, taxonomy.ErrReleaseConflict) {
		t.Fatal("откат затёр последующую операторскую публикацию", err)
	}
}
