package taxonomy_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func schema(version int64) taxonomy.Definition {
	return taxonomy.Definition{
		Attributes: []taxonomy.Attribute{
			{
				Code:       "a1",
				Name:       "Атрибут",
				Type:       "enum",
				Values:     []string{"v1"},
				Facet:      true,
				Dictionary: "a1",
			},
		},
		Schemas: map[string]taxonomy.Schema{"s1": {Attributes: []string{"a1"}, Optional: []string{"a1"}}},
		Version: version,
		Categories: []taxonomy.Category{
			{
				ID:     "c1",
				Slug:   "c1",
				Name:   "Категория",
				Schema: "s1",
			},
		},
	}
}

func TestLoadAndRevalidate(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	seedValues(t, store, "v1")
	clock := fake.NewClock(time.Now())
	service := apptaxonomy.NewService(store, clock, nil)
	projector := appcatalog.NewProjector(store, clock, nil)
	if err := applyDefinition(ctx, service, schema(1)); err != nil {
		t.Fatal(err)
	}
	event := appcatalog.ProductEvent{
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
					Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`"v1"`)},
				},
			},
		},
	}
	if err := projector.ApplyProduct(ctx, event); err != nil {
		t.Fatal(err)
	}
	p, _ := store.Product("t1", "p1")
	if !p.Card.Publication.Published || p.Card.Publication.Version != 1 {
		t.Fatalf("%+v", p)
	}
	if err := applyDefinition(ctx, service, schema(1)); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := store.Product("t1", "p1")
	if unchanged.Version != p.Version {
		t.Fatal("идемпотентная загрузка меняет проекцию")
	}
	next := schema(2)
	next.Attributes[0].Values = []string{"v2"}
	seedValues(t, store, "v2")
	store.FailCommit = errors.New("сбой фиксации")
	if err := applyDefinition(ctx, service, next); err == nil {
		t.Fatal("ожидался откат")
	}
	store.FailCommit = nil
	current, err := service.Current(ctx)
	if err != nil || current.Version != 1 {
		t.Fatalf("%+v %v", current, err)
	}
	if err := applyDefinition(ctx, service, next); err != nil {
		t.Fatal(err)
	}
	p, _ = store.Product("t1", "p1")
	if p.Card.Publication.Published || p.Card.Publication.Issues[0].Code != "taxonomy.enum_value" {
		t.Fatalf("ошибка должна скрыть карточку: %+v", p)
	}
	if len(store.OutboxRows()) != 2 || !store.Processed("e1") {
		t.Fatal("нет атомарных outbox/inbox")
	}
	if err := applyDefinition(ctx, service, schema(1)); !errors.Is(err, taxonomy.ErrReleaseConflict) {
		t.Fatal(err)
	}
	if err := applyDefinition(ctx, service, schema(2)); !errors.Is(err, taxonomy.ErrReleaseConflict) {
		t.Fatal(err)
	}
	seedValues(t, store, "v1")
	// Новая версия восстанавливает карточку из исходного блока без нового события магазина.
	if err := applyDefinition(ctx, service, schema(3)); err != nil {
		t.Fatal(err)
	}
	p, _ = store.Product("t1", "p1")
	if !p.Card.Publication.Published || p.Card.Publication.Version != 3 {
		t.Fatalf("%+v", p)
	}
	if err := projector.ApplyProduct(ctx, event); err != nil {
		t.Fatal(err)
	}
	again, _ := store.Product("t1", "p1")
	if again.Version != p.Version {
		t.Fatal("повтор события изменяет проекцию")
	}
}

func TestInvalidCardIsStoredAndNotParked(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	seedValues(t, store, "v1")
	clock := fake.NewClock(time.Now())
	projector := appcatalog.NewProjector(store, clock, nil)
	for i, card := range []*taxonomy.Card{nil, {CategoryID: "missing", Published: true}} {
		event := appcatalog.ProductEvent{
			EventID: string(rune('a' + i)),
			Snapshot: catalog.Snapshot{
				TenantID:  "t1",
				ProductID: "p1",
				Revision:  int64(i + 1),
				At:        clock.Now(),
				Card:      catalog.Card{Name: "Товар", Visible: true, Marketplace: card},
			},
		}
		if err := projector.ApplyProduct(ctx, event); err != nil {
			t.Fatal(err)
		}
		p, _ := store.Product("t1", "p1")
		if p.Card.Publication.Published || len(p.Card.Publication.Issues) != 1 {
			t.Fatalf("%+v", p)
		}
		queries := appcatalog.NewQueries(
			store,
			fake.NewIndex(),
			fake.NewPlatform(),
			nil,
			nil,
			fake.NewClock(time.Time{}),
		)
		publication, err := queries.Publication(
			ctx,
			ids.OfferID{Tenant: "t1", Product: "p1"},
		)
		if err != nil || publication.Published {
			t.Fatalf("%+v %v", publication, err)
		}
	}
}

func TestRevalidationPagesIncludeNonParticipants(t *testing.T) {
	store := fake.NewStore()
	seedValues(t, store, "v1")
	clock := fake.NewClock(time.Now())
	for i := 0; i < 205; i++ {
		store.SeedProduct(catalog.ProductState{
			TenantID:  "t1",
			ProductID: ids.ProductID(fmt.Sprintf("p%03d", i)),
			Revision:  1,
			Version:   1,
			Card:      catalog.Card{Name: "Товар", Visible: true},
		})
	}
	if err := applyDefinition(context.Background(), apptaxonomy.NewService(store, clock, nil), schema(1)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		p, _ := store.Product("t1", ids.ProductID(fmt.Sprintf("p%03d", i)))
		if p.Version != 2 || p.Card.Publication.Version != 1 || p.Card.Publication.Published {
			t.Fatalf("товар пропущен при перепроверке: %+v", p)
		}
	}
	if len(store.OutboxRows()) != 410 {
		t.Fatal(len(store.OutboxRows()))
	}
}

// applyDefinition сохраняет проверку повторов поставки в сценариях перепроверки карточек.
func applyDefinition(ctx context.Context, service *apptaxonomy.Service, d taxonomy.Definition) error {
	body, _ := json.Marshal(d)
	checksum := sha256.Sum256(body)
	_, err := service.Apply(ctx, taxonomy.Release{
		Version:    d.Version,
		Checksum:   hex.EncodeToString(checksum[:]),
		Attributes: d.Attributes,
		Schemas:    d.Schemas,
		Categories: d.Categories,
	})
	return err
}

func seedValues(t *testing.T, store *fake.Store, values ...string) {
	t.Helper()
	state := reference.DictionaryState{
		Code:   "a1",
		Kind:   reference.KindClosed,
		Limits: reference.DefaultLimits(),
	}
	store.SeedReferenceDictionary(state)
	err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		old, err := tx.Reference().AllEntries(ctx, "a1")
		if err != nil {
			return err
		}
		entries := []*reference.Entry{}
		for i, value := range values {
			entry, err := reference.NewEntry(
				reference.RestoreDictionary(state),
				reference.NewEntryInput{
					ID:               ids.UUID(fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)),
					CanonicalAliasID: ids.UUID(fmt.Sprintf("00000000-0000-4000-9000-%012d", i+1)),
					Name:             value,
					Status:           reference.StatusConfirmed,
					Source:           reference.SourceSeed,
					Metadata:         json.RawMessage(fmt.Sprintf(`{"order":%d}`, len(values)-i)),
				},
				time.Now(),
			)
			if err != nil {
				return err
			}
			entry.Bump(time.Now())
			entries = append(entries, entry)
		}
		return tx.Reference().Save(ctx, entries, old)
	})
	if err != nil {
		t.Fatal(err)
	}
}
