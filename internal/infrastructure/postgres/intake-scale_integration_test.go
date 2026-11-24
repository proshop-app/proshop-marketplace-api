//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	intakeScaleTenants = 100
	// intakeScaleBatch — размер пачки снимков, как у приёма Kafka.
	intakeScaleBatch = 50
	// cloneIntakeCatalog размножает товары-образцы, принятые сценарием, до размера каталога.
	cloneIntakeCatalog = `INSERT INTO catalog_products (
        tenant_id, product_id, revision, deleted, card, price, base_price, available, projected_at, version, updated_at
    )
    SELECT 's' || lpad((n % 1000)::text, 4, '0'), 'c' || n, p.revision, p.deleted, p.card, p.price, p.base_price,
        p.available, p.projected_at, p.version, p.updated_at
    FROM catalog_products p
    CROSS JOIN generate_series($1::bigint, $2::bigint) n
    WHERE p.tenant_id = 't0' AND p.product_id = 'p0'`
)

type statementCounter struct {
	statements atomic.Int64
	print      atomic.Int64
}

func (c *statementCounter) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	c.statements.Add(1)
	if c.print.Add(-1) >= 0 {
		statement := strings.Join(strings.Fields(data.SQL), " ")
		fmt.Println("SQL:", statement[:min(160, len(statement))])
	}
	return ctx
}

func (*statementCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// TestProjectionIntakeAtScale замеряет приём снимков товара одним исполнителем на настоящем PostgreSQL:
// сообщений в секунду и операторов SQL на сообщение. INTAKE_SCALE_CATALOG — размер каталога вокруг.
func TestProjectionIntakeAtScale(t *testing.T) {
	if os.Getenv("INTAKE_SCALE") != "1" {
		t.Skip("замер: INTAKE_SCALE=1")
	}
	catalogSize, _ := strconv.Atoi(os.Getenv("INTAKE_SCALE_CATALOG"))
	messages, _ := strconv.Atoi(os.Getenv("INTAKE_SCALE_MESSAGES"))
	messages = max(messages, 2000)
	ctx := context.Background()
	base := testPool(t)
	config := base.Config().Copy()
	counter := &statementCounter{}
	config.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uow := postgres.NewUnitOfWork(pool)
	projector := seedProjectionIntake(ctx, t, uow, pool, catalogSize)
	for _, batch := range []int{1, intakeScaleBatch} {
		for _, pass := range []struct {
			name     string
			revision int64
		}{{name: "new", revision: 1}, {name: "update", revision: 2}, {name: "repeat", revision: 2}} {
			counter.statements.Store(0)
			if os.Getenv("INTAKE_SCALE_TRACE") == "1" {
				counter.print.Store(20)
			}
			started := time.Now()
			applyIntakePass(ctx, t, projector, messages, batch, pass.revision)
			elapsed := time.Since(started)
			rate := float64(messages) / elapsed.Seconds()
			perMessage := float64(counter.statements.Load()) / float64(messages)
			summary := "MEASURE batch=%d pass=%s catalog=%d n=%d elapsed=%s rate=%.1f msg/s sql=%.2f/msg"
			t.Logf(summary, batch, pass.name, catalogSize, messages, elapsed, rate, perMessage)
		}
	}
}

// applyIntakePass применяет снимки пачками размера batch; товары прохода свои у каждого размера пачки.
func applyIntakePass(
	ctx context.Context,
	t *testing.T,
	projector *appcatalog.Projector,
	messages, batch int,
	revision int64,
) {
	t.Helper()
	events := make([]appcatalog.ProductEvent, 0, batch)
	for n := range messages {
		events = append(events, intakeScaleEvent(n, revision, fmt.Sprintf("b%d-", batch)))
		if len(events) < batch && n < messages-1 {
			continue
		}
		if err := projector.ApplyProducts(ctx, events); err != nil {
			t.Fatal(err)
		}
		events = events[:0]
	}
}

func seedProjectionIntake(
	ctx context.Context,
	t *testing.T,
	uow ports.UnitOfWork,
	pool *pgxpool.Pool,
	catalogSize int,
) *appcatalog.Projector {
	t.Helper()
	registry := appparticipant.NewRegistry(uow, clock.System{}, random.IDs{})
	for n := range intakeScaleTenants {
		tenant := ids.TenantID(fmt.Sprintf("t%d", n))
		event := participant.RegistryEvent{
			TenantID: tenant,
			Slug:     string(tenant),
			Name:     "Магазин",
			Status:   participant.TenantActive,
			At:       time.Now(),
		}
		err := registry.Apply(ctx, appparticipant.RegistryEvent{EventID: string(tenant), Event: event})
		if err != nil {
			t.Fatal(err)
		}
	}
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		definition := taxonomy.Definition{
			Version:    1,
			Schemas:    map[string]taxonomy.Schema{"s1": {Attributes: []string{}, Optional: []string{}}},
			Categories: []taxonomy.Category{{ID: "c1", Slug: "c1", Name: "Категория", Schema: "s1"}},
		}
		return tx.Taxonomy().Save(ctx, definition, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE participants SET trust = 'trusted'`); err != nil {
		t.Fatal(err)
	}
	moderation := appmoderation.NewService(uow, clock.System{}, random.IDs{}, appmoderation.Config{})
	projector := appcatalog.NewProjector(uow, clock.System{}, moderation)
	if err := projector.ApplyProduct(ctx, intakeScaleEvent(0, 1, "")); err != nil {
		t.Fatal(err)
	}
	if catalogSize > 0 {
		// Пачками: тестовый пул держит срок оператора 10 с.
		for first := 1; first <= catalogSize; first += 50000 {
			if _, err := pool.Exec(ctx, cloneIntakeCatalog, first, min(catalogSize, first+49999)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := pool.Exec(ctx, "ANALYZE catalog_products"); err != nil {
			t.Fatal(err)
		}
	}
	return projector
}

func intakeScaleEvent(n int, revision int64, prefix string) appcatalog.ProductEvent {
	tenant := ids.TenantID(fmt.Sprintf("t%d", n%intakeScaleTenants))
	product := ids.ProductID(fmt.Sprintf("%sp%d", prefix, n))
	price := money.FromKopecks(10000)
	return appcatalog.ProductEvent{
		Snapshot: catalog.Snapshot{
			TenantID:  tenant,
			ProductID: product,
			Revision:  revision,
			HasPrice:  true,
			Price:     price,
			BasePrice: price,
			Available: 5,
			Card: catalog.Card{
				Name:    fmt.Sprintf("Товар %d", revision),
				Visible: true,
				Marketplace: &taxonomy.Card{
					CategoryID: "c1",
					Published:  true,
				},
			},
			At: time.Now(),
		},
	}
}
