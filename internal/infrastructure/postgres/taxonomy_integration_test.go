//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const countTaxonomyVersions = `SELECT count(*) FROM taxonomy_versions`

func testTaxonomy(version int64) taxonomy.Definition {
	return taxonomy.Definition{Version: version, Categories: []taxonomy.Category{{
		ID:   "c1",
		Slug: "c1",
		Name: "Категория",
		Attributes: []taxonomy.Attribute{{
			Code:     "a1",
			Name:     "Атрибут",
			Type:     "number",
			Required: true,
			Facet:    true,
		}},
	}}}
}

func TestTaxonomyVersionsAndProjection(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	now := clock.System{}
	service := apptaxonomy.NewService(uow, now, nil)
	for i := 0; i < 2; i++ {
		if err := service.Load(ctx, testTaxonomy(1)); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, countTaxonomyVersions).Scan(&count); err != nil || count != 1 {
		t.Fatalf("%d %v", count, err)
	}
	projector := appcatalog.NewProjector(uow, now, nil)
	if err := projector.ApplyProduct(ctx, appcatalog.ProductEvent{
		EventID: "e1",
		Snapshot: catalog.Snapshot{
			TenantID:  "t1",
			ProductID: "p1",
			Revision:  1,
			At:        now.Now(),
			Card: catalog.Card{Name: "Товар", Visible: true, Marketplace: &taxonomy.Card{
				CategoryID: "c1",
				Published:  true,
				Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`1.5`)},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	read := func() taxonomy.Publication {
		t.Helper()
		queries := appcatalog.NewQueries(uow, nil, fake.NewPlatform())
		p, err := queries.Publication(ctx, ids.OfferID{Tenant: "t1", Product: "p1"})
		if err != nil {
			t.Fatal(err)
		}
		return p.Publication
	}
	if p := read(); !p.Published || string(p.Attributes["a1"]) != "1.5" {
		t.Fatalf("%+v", p)
	}
	next := testTaxonomy(2)
	next.Categories[0].Attributes = append(next.Categories[0].Attributes, taxonomy.Attribute{
		Code:     "a2",
		Name:     "Атрибут 2",
		Type:     "boolean",
		Required: true,
	})
	if err := service.Load(ctx, next); err != nil {
		t.Fatal(err)
	}
	if p := read(); p.Published || p.Issues[0].Code != "taxonomy.required" || p.Version != 2 {
		t.Fatalf("%+v", p)
	}
	if err := service.Load(ctx, testTaxonomy(1)); !errors.Is(err, taxonomy.ErrConflict) {
		t.Fatal(err)
	}
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Taxonomy().Save(ctx, testTaxonomy(3), 1)
	})
	if !errors.Is(err, taxonomy.ErrConflict) {
		t.Fatal(err)
	}
	current, err := service.Current(ctx)
	if err != nil || current.Version != 2 {
		t.Fatalf("%+v %v", current, err)
	}
}

func TestTaxonomySharedLockSerializesLoad(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	locked := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			if _, err := tx.Taxonomy().Current(ctx); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	consumed := false
	defer func() {
		close(release)
		if consumed {
			return
		}
		if err := <-finished; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-locked:
	case err := <-finished:
		consumed = true
		t.Fatal(err)
	}
	limited, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := apptaxonomy.NewService(uow, clock.System{}, nil).Load(limited, testTaxonomy(1)); err == nil {
		t.Fatal("загрузка обошла разделяемую блокировку приёма карточки")
	}
}
