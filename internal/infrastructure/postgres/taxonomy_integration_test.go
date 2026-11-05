//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	return taxonomy.Definition{
		Attributes: []taxonomy.Attribute{
			{
				Code:  "a1",
				Name:  "Атрибут",
				Type:  "number",
				Facet: true,
			},
		},
		Schemas: map[string]taxonomy.Schema{"s1": {Attributes: []string{"a1"}, Optional: []string{}}},
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

func TestTaxonomyVersionsAndProjection(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	now := clock.System{}
	service := apptaxonomy.NewService(uow, now)
	for i := 0; i < 2; i++ {
		if err := applyDefinition(ctx, service, testTaxonomy(1)); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, countTaxonomyVersions).Scan(&count); err != nil || count != 1 {
		t.Fatalf("%d %v", count, err)
	}
	projector := appcatalog.NewProjector(uow, now)
	if err := projector.ApplyProduct(ctx, appcatalog.ProductEvent{
		EventID: "e1",
		Snapshot: catalog.Snapshot{
			TenantID:  "t1",
			ProductID: "p1",
			Revision:  1,
			At:        now.Now(),
			Card: catalog.Card{
				Name:    "Товар",
				Visible: true,
				Marketplace: &taxonomy.Card{
					CategoryID: "c1",
					Published:  true,
					Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`1.5`)},
				},
			},
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
		return p
	}
	if p := read(); !p.Published || string(p.Attributes["a1"]) != "1.5" {
		t.Fatalf("%+v", p)
	}
	next := testTaxonomy(2)
	next.Attributes = append(next.Attributes, taxonomy.Attribute{
		Code: "a2",
		Name: "Атрибут 2",
		Type: "boolean",
	})
	next.Schemas["s1"] = taxonomy.Schema{Attributes: []string{"a1", "a2"}}
	if err := applyDefinition(ctx, service, next); err != nil {
		t.Fatal(err)
	}
	if p := read(); p.Published || p.Issues[0].Code != "taxonomy.required" || p.Version != 2 {
		t.Fatalf("%+v", p)
	}
	if err := applyDefinition(ctx, service, testTaxonomy(1)); !errors.Is(err, taxonomy.ErrReleaseConflict) {
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
	if err := applyDefinition(limited, apptaxonomy.NewService(uow, clock.System{}), testTaxonomy(1)); err == nil {
		t.Fatal("загрузка обошла разделяемую блокировку приёма карточки")
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
