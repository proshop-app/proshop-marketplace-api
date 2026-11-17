//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	throughputPostgresVersion = `SHOW server_version`
	seedThroughputProducts    = `INSERT INTO catalog_products
        (tenant_id, product_id, revision, deleted, card, price, available,
            projected_at, version, updated_at, price_profiles)
        SELECT 't' || t, 'p' || lpad(n::text, 5, '0'), 1, false, $1::jsonb -> ((n-1) % 10), 100, 1,
            now(), 1, now(), '[]'::jsonb FROM generate_series(1,100) t CROSS JOIN generate_series(1,200) n`
)

// TestTaxonomyRevalidationThroughput запускается явно: замер сохраняет рабочую паузу, без -race.
func TestTaxonomyRevalidationThroughput(t *testing.T) {
	if os.Getenv("TAXONOMY_REVALIDATION_BENCH") != "1" {
		t.Skip("явный замер: TAXONOMY_REVALIDATION_BENCH=1")
	}
	pool := pgtest.Pool(t)
	var version string
	if err := pool.QueryRow(context.Background(), throughputPostgresVersion).Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Log("PostgreSQL server_version:", version)
	uow := postgres.NewUnitOfWork(pool)
	evaluator := appmoderation.NewService(uow, clock.System{}, random.IDs{}, appmoderation.Config{})
	service := apptaxonomy.NewService(uow, clock.System{}, evaluator)
	definition, cards := throughputDefinition(t)
	if err := applyDefinition(context.Background(), service, definition); err != nil {
		t.Fatal(err)
	}
	drainTaxonomyPages(t, service)
	for i := 1; i <= 100; i++ {
		seedRevalidationMember(t, uow, ids.TenantID(fmt.Sprintf("t%d", i)))
	}
	if _, err := pool.Exec(context.Background(), seedThroughputProducts, string(cards)); err != nil {
		t.Fatal(err)
	}
	definition.Attributes[0].Name = "Изменённое поле"
	if _, err := service.LoadPart(context.Background(), "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	work := time.Duration(0)
	pages := 0
	for {
		before := time.Now()
		job, err := service.RevalidateNext(context.Background())
		work += time.Since(before)
		if err != nil {
			t.Fatal(err)
		}
		if job == nil {
			break
		}
		pages++
		time.Sleep(100 * time.Millisecond)
	}
	elapsed := time.Since(started)
	failures, err := service.RevalidationFailures(context.Background(), ports.OfferKey{}, 100)
	if err != nil || len(failures) != 0 {
		t.Fatal(failures, err)
	}
	var count int
	if err := pool.QueryRow(context.Background(), countBatchVerdicts).Scan(&count); err != nil || count != 20000 {
		t.Fatal("замер пропустил модерацию", count, err)
	}
	rate := 20000 / elapsed.Seconds()
	message := "PostgreSQL16: cards=20000 tenants=100 categories=10 fields=8 pages=%d elapsed=%s work=%s rate=%.2f/s"
	t.Logf(message, pages, elapsed, work, rate)
	million := time.Duration(1e6 / rate * float64(time.Second))
	tenMillion := time.Duration(1e7 / rate * float64(time.Second))
	t.Logf("projection: 1M=%s 10M=%s", million, tenMillion)
}

func throughputDefinition(t *testing.T) (taxonomy.Definition, []byte) {
	t.Helper()
	definition := testTaxonomy(1)
	schema := definition.Schemas["s1"]
	schema.Optional = []string{"a1"}
	for i := 2; i <= 8; i++ {
		code := fmt.Sprintf("a%d", i)
		field := taxonomy.Attribute{Code: code, Name: code, Type: "string"}
		definition.Attributes = append(definition.Attributes, field)
		schema.Attributes = append(schema.Attributes, code)
		schema.Optional = append(schema.Optional, code)
	}
	definition.Schemas["s1"] = schema
	definition.Categories = nil
	cards := []map[string]any{}
	for i := 1; i <= 10; i++ {
		category := fmt.Sprintf("c%d", i)
		definition.Categories = append(definition.Categories, taxonomy.Category{
			ID:     category,
			Slug:   category,
			Name:   fmt.Sprintf("Категория %d", i),
			Schema: "s1",
		})
	}
	for _, category := range definition.Categories {
		card := &taxonomy.Card{
			CategoryID: category.ID,
			Published:  true,
			Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`1`)},
		}
		for i := 2; i <= 8; i++ {
			card.Attributes[fmt.Sprintf("a%d", i)] = json.RawMessage(`"value"`)
		}
		cards = append(cards, map[string]any{
			"name":        "Товар",
			"visible":     true,
			"assets":      []string{},
			"marketplace": card,
			"publication": definition.Check(card),
		})
	}
	raw, err := json.Marshal(cards)
	if err != nil {
		t.Fatal(err)
	}
	return definition, raw
}
