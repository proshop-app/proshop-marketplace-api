package taxonomy_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestPartPublicationFreezesDictionary(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	store.SeedTaxonomy(schema(1))
	seedValues(t, store, "v1", "v2")
	service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil)
	input := schema(1)
	input.Attributes[0].Values = []string{"forged"}
	store.FailCommit = errors.New("сбой фиксации")
	if _, err := service.LoadPart(ctx, "attributes", input, 1); err == nil {
		t.Fatal("ошибка фиксации скрыта")
	}
	store.FailCommit = nil
	before, _ := service.Current(ctx)
	if before.Version != 1 {
		t.Fatal("частичная публикация", before.Version)
	}
	version, err := service.LoadPart(ctx, "attributes", input, 1)
	if err != nil || version != 2 {
		t.Fatalf("%d %v", version, err)
	}
	current, _ := service.Current(ctx)
	if !reflect.DeepEqual(current.Attributes[0].Values, []string{"v2", "v1"}) {
		t.Fatalf("порядок реестра потерян: %+v", current.Attributes)
	}
	seedValues(t, store, "v3")
	unchanged, _ := service.Current(ctx)
	if !reflect.DeepEqual(current, unchanged) {
		t.Fatal("реестр изменил опубликованный снимок")
	}
	if _, err := service.LoadPart(ctx, "schemas", input, 1); !errors.Is(err, taxonomy.ErrConflict) {
		t.Fatal("устаревшая версия принята", err)
	}
	if _, err := service.LoadPart(ctx, "schemas", input, 2); err != nil {
		t.Fatal(err)
	}
	current, _ = service.Current(ctx)
	if !reflect.DeepEqual(current.Attributes[0].Values, []string{"v3"}) {
		t.Fatal(current.Attributes)
	}
}

func TestDictionaryFailuresDoNotPublish(t *testing.T) {
	for _, name := range []string{"missing", "kind", "empty", "metadata"} {
		t.Run(name, func(t *testing.T) {
			store := fake.NewStore()
			original := schema(1)
			store.SeedTaxonomy(original)
			input := schema(1)
			switch name {
			case "kind":
				store.SeedReferenceDictionary(reference.DictionaryState{Code: "a1", Kind: reference.KindOpen})
			case "empty":
				store.SeedReferenceDictionary(reference.DictionaryState{Code: "a1", Kind: reference.KindClosed})
			case "metadata":
				seedValues(t, store, "v2")
				input.Attributes[0].Values = nil
				input.Attributes[0].ValuesMeta = map[string]taxonomy.ValueMeta{"v1": {Hex: "#FFFFFF"}}
			}
			service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil)
			_, err := service.LoadPart(context.Background(), "attributes", input, 1)
			if !errors.Is(err, taxonomy.ErrInvalid) {
				t.Fatal("неверный словарь принят", err)
			}
			current, _ := service.Current(context.Background())
			if !reflect.DeepEqual(original, current) || len(store.OutboxRows()) != 0 {
				t.Fatal("частичная публикация")
			}
		})
	}
}

func TestOpenDictionaryDoesNotRestrictStrings(t *testing.T) {
	store := fake.NewStore()
	input := schema(1)
	input.Attributes[0].Type = "string"
	input.Attributes[0].Values = nil
	store.SeedTaxonomy(input)
	store.SeedReferenceDictionary(reference.DictionaryState{Code: "a1", Kind: reference.KindOpen})
	service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil)
	if _, err := service.LoadPart(context.Background(), "attributes", input, 1); err != nil {
		t.Fatal(err)
	}
	current, _ := service.Current(context.Background())
	if len(current.Attributes[0].Values) != 0 {
		t.Fatal("открытый словарь стал перечислением")
	}
}

func TestConcurrentPartsHaveOneWinner(t *testing.T) {
	store := fake.NewStore()
	store.SeedTaxonomy(schema(1))
	seedValues(t, store, "v1")
	service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil)
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, part := range []string{"categories", "schemas"} {
		group.Add(1)
		go func(part string) {
			defer group.Done()
			_, err := service.LoadPart(context.Background(), part, schema(1), 1)
			results <- err
		}(part)
	}
	group.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, taxonomy.ErrConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
}

func TestRollbackRejectsOldFormat(t *testing.T) {
	store := fake.NewStore()
	store.SeedTaxonomy(taxonomy.Definition{
		Version:    1,
		Categories: []taxonomy.Category{{ID: "c1", Name: "Категория", Slug: "c1"}},
	})
	seedValues(t, store, "v1")
	service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil)
	next := schema(2)
	release := taxonomy.Release{
		Version:         4,
		Checksum:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Attributes:      next.Attributes,
		Schemas:         next.Schemas,
		Categories:      next.Categories,
		ExpectedVersion: 1,
	}
	if _, err := service.Apply(context.Background(), release); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rollback(context.Background(), release); !errors.Is(err, taxonomy.ErrConflict) {
		t.Fatal(err)
	}
	current, _ := service.Current(context.Background())
	if current.Version != 2 {
		t.Fatal(current.Version)
	}
}

func TestRollbackRestoresFrozenValues(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	store.SeedTaxonomy(schema(1))
	seedValues(t, store, "v2")
	service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil)
	next := schema(2)
	release := taxonomy.Release{
		Version:         4,
		Checksum:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Attributes:      next.Attributes,
		Schemas:         next.Schemas,
		Categories:      next.Categories,
		ExpectedVersion: 1,
	}
	if _, err := service.Apply(ctx, release); err != nil {
		t.Fatal(err)
	}
	seedValues(t, store, "v3")
	result, err := service.Rollback(ctx, release)
	if err != nil || result.SnapshotVersion != 3 {
		t.Fatal(result, err)
	}
	current, _ := service.Current(ctx)
	if !reflect.DeepEqual(current.Attributes[0].Values, []string{"v1"}) {
		t.Fatal(current.Attributes)
	}
}

func TestOnlyConfirmedValuesArePublished(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	store.SeedTaxonomy(schema(1))
	seedValues(t, store, "v1", "v2")
	err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		entries, err := tx.Reference().AllEntries(ctx, "a1")
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.State().CanonicalName != "v2" {
				continue
			}
			if _, err := entry.ChangeStatus(reference.StatusDeprecated, time.Now()); err != nil {
				return err
			}
			entry.Bump(time.Now())
			return tx.Reference().Save(ctx, []*reference.Entry{entry}, nil)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil)
	if _, err := service.LoadPart(ctx, "categories", schema(1), 1); err != nil {
		t.Fatal(err)
	}
	current, _ := service.Current(ctx)
	if !reflect.DeepEqual(current.Attributes[0].Values, []string{"v1"}) {
		t.Fatal(current.Attributes)
	}
}
