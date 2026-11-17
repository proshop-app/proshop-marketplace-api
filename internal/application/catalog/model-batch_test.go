package catalog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type batchModelResolver struct {
	calls  int
	size   int
	before func()
}

type modelBatchUOW struct {
	ports.UnitOfWork
	calls int
}

func (r *batchModelResolver) Resolve(context.Context, string) (string, bool, error) {
	panic("одиночное разрешение не должно вызываться")
}

func (r *batchModelResolver) ResolveDictionaryBatch(
	_ context.Context,
	_ string,
	texts []string,
) (map[string]string, error) {
	r.calls++
	r.size = len(texts)
	if r.before != nil {
		r.before()
	}
	return map[string]string{"Марка": "brand-id"}, nil
}

func (u *modelBatchUOW) Do(ctx context.Context, run func(context.Context, ports.Tx) error) error {
	u.calls++
	return u.UnitOfWork.Do(ctx, run)
}

func TestModelKeysBatchUniqueValuesAndVersionConflicts(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	definition := taxonomy.Definition{
		Attributes: []taxonomy.Attribute{{Code: "brand", Type: "string", Dictionary: "brand"}},
		Categories: []taxonomy.Category{{ID: "c1", Schema: "s1"}},
		Schemas: map[string]taxonomy.Schema{
			"s1": {
				Attributes: []string{"brand"},
				Identity:   &taxonomy.SchemaIdentity{Model: []string{"brand"}},
			},
		},
	}
	seedSeller(store, "t1", participant.TenantActive, true)
	store.SeedTaxonomy(definition)
	for n := 0; n < 100; n++ {
		id := ids.ProductID(fmt.Sprintf("p%03d", n))
		seedProduct(store, "t1", id)
		state, _ := store.Product("t1", id)
		state.Card.Publication.CategoryID = "c1"
		state.Card.Publication.Attributes = map[string]json.RawMessage{"brand": json.RawMessage(`"Марка"`)}
		store.SeedProduct(state)
	}

	resolver := &batchModelResolver{
		before: func() {
			state, _ := store.Product("t1", "p000")
			state.Version++
			state.Card.Name = "Новая карточка"
			store.SeedProduct(state)
		},
	}
	uow := &modelBatchUOW{UnitOfWork: store}
	index := fake.NewIndex()
	indexer := appcatalog.NewIndexer(
		uow,
		index,
		fake.NewClock(t1),
		200,
		&fake.IndexRebuildLock{},
		appcatalog.NewModelKeys(resolver, nil, true),
		time.Second,
	)
	err := indexer.SyncTenant(ctx, "t1")
	if err != nil || resolver.calls != 1 || resolver.size != 1 || uow.calls != 3 {
		t.Fatalf("resolves=%d size=%d transactions=%d err=%v", resolver.calls, resolver.size, uow.calls, err)
	}
	if _, indexed := index.Doc("t1~p000"); indexed {
		t.Fatal("конфликтующий снимок попал в индекс")
	}
	conflict, _ := store.Product("t1", "p000")
	saved, _ := store.Product("t1", "p001")
	if conflict.Card.Name != "Новая карточка" || conflict.Card.ModelKey != "" || saved.Card.ModelKey != "s1:brand-id" {
		t.Fatal(conflict, saved)
	}
}
