//go:build integration

package postgres_test

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const (
	bulkInterruptedCaseInsert = `INSERT INTO moderation_cases
        (id, tenant_id, product_id, revision, source, status, reasons, note, opened_at, version)
        VALUES ('00000000-0000-4000-8000-000000000001', 't1', 'p1', 1, 'rule', 'open',
            '[{"key":"moderation.probation"}]', '', now(), 1)`
	interruptedIndexValid   = `SELECT indisvalid FROM pg_index WHERE indexrelid = to_regclass($1)`
	preservedPickupIndexOID = `SELECT to_regclass($1)::oid`
	preservedIndexOID       = `SELECT 'outbox_quarantine_id_idx'::regclass::oid`
	indexBuildStarted       = `SELECT EXISTS (
        SELECT 1 FROM pg_index WHERE indexrelid = to_regclass($1) AND NOT indisvalid
    )`
	interruptedOutboxInsert = `INSERT INTO outbox (kind, tenant_id, payload, next_at, created_at)
        VALUES ('test', 't1', '{}', now(), now())`
)

type interruptedIndexCase struct {
	version int64
	name    string
	file    string
	insert  string
}

func TestInterruptedConcurrentIndexMigrationRecovers(t *testing.T) {
	tests := []interruptedIndexCase{
		{
			version: 62,
			name:    "moderation_case_probation",
			file:    "migrations/00062_moderation_bulk.sql",
			insert:  bulkInterruptedCaseInsert,
		},
		{
			version: 62,
			name:    "moderation_case_tenant_open",
			file:    "migrations/00062_moderation_bulk.sql",
			insert:  bulkInterruptedCaseInsert,
		},
		{
			version: 62,
			name:    "moderation_case_open_id",
			file:    "migrations/00062_moderation_bulk.sql",
			insert:  bulkInterruptedCaseInsert,
		},
		{
			version: 62,
			name:    "moderation_case_source_open_id",
			file:    "migrations/00062_moderation_bulk.sql",
			insert:  bulkInterruptedCaseInsert,
		},
		{
			version: 62,
			name:    "moderation_case_open_reasons",
			file:    "migrations/00062_moderation_bulk.sql",
			insert:  bulkInterruptedCaseInsert,
		},
		{
			version: 54,
			name:    "catalog_products_store_summary",
			file:    "migrations/00054_marketplace_read_indexes.sql",
			insert: `INSERT INTO catalog_products (
                tenant_id, product_id, revision, deleted, projected_at, version, updated_at
            ) VALUES ('t1', 'p1', 1, true, now(), 1, now())`,
		},
		{
			version: 55,
			name:    "parked_events_resolved_updated_idx",
			file:    "migrations/00055_resolved_event_retention.sql",
			insert: `INSERT INTO parked_events (topic, kafka_partition, kafka_offset, reason, parked_at)
                VALUES ('test', 0, 1, 'test', now())`,
		},
		{
			version: 56,
			name:    "outbox_pending_due_idx",
			file:    "migrations/00056_outbox_quarantine.sql",
			insert:  interruptedOutboxInsert,
		},
		{
			version: 57,
			name:    "outbox_retry_kind_created_idx",
			file:    "migrations/00057_outbox_retry_stats.sql",
			insert:  interruptedOutboxInsert,
		},
		{
			version: 59,
			name:    "participants_pickup_cdek",
			file:    "migrations/00059_pickup_source_indexes.sql",
			insert:  "UPDATE participants SET version = version",
		},
		{
			version: 59,
			name:    "participants_pickup_yandex",
			file:    "migrations/00059_pickup_source_indexes.sql",
			insert:  "UPDATE participants SET version = version",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { testInterruptedIndexRecovery(t, test) })
	}
}

func testInterruptedIndexRecovery(t *testing.T, test interruptedIndexCase) {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	if err := postgres.MigrateDownTo(ctx, pool, test.version-1); err != nil {
		t.Fatal(err)
	}
	if test.version == 56 {
		ddl := `ALTER TABLE outbox ADD COLUMN failed_at timestamptz,
            ADD COLUMN failure_reason text, ADD COLUMN unknown_failures integer`
		if _, err := pool.Exec(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(test.file)
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Split(string(raw), "-- +goose Down")[0]
	if test.version == 62 {
		function := regexp.MustCompile(`(?s)CREATE OR REPLACE FUNCTION moderation_reason_keys\b.*?\$\$;`)
		if _, err := pool.Exec(ctx, function.FindString(up)); err != nil {
			t.Fatal(err)
		}
	}
	pattern := regexp.MustCompile(`(?s)CREATE INDEX CONCURRENTLY IF NOT EXISTS ` + test.name + `\b[^;]+;`)
	statement := pattern.FindString(up)
	if statement == "" {
		t.Fatal("нет индексного DDL", test.name)
	}
	var preserved uint32
	if test.version == 56 {
		validPattern := `(?s)CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_quarantine_id_idx\b[^;]+;`
		validDDL := regexp.MustCompile(validPattern).FindString(up)
		if _, err := pool.Exec(ctx, validDDL); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, preservedIndexOID).Scan(&preserved); err != nil {
			t.Fatal(err)
		}
	}
	interruptIndexBuild(t, pool, statement, test.insert, test.name)
	var valid bool
	err = pool.QueryRow(ctx, interruptedIndexValid, test.name).Scan(&valid)
	if err != nil || valid {
		t.Fatal("не воспроизведён INVALID", valid, err)
	}
	if test.version == 56 {
		// SQL без восстановления обязан сохранить старые индексы при INVALID.
		if err := migrateUpTo(ctx, pool, test.version); err == nil {
			t.Fatal("миграция приняла негодный индекс и удалила старые")
		}
		for _, old := range []string{"outbox_due_idx", "outbox_tenant_due_idx"} {
			assertValidIndex(t, pool, old)
		}
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	index := test.name
	switch test.version {
	case 59:
		// Единый перечень служб заменяет оба прежних индекса одним индексом видимости.
		index = "participants_pickup_visible"
	case 56:
		// Полосы 00070 заменяют индексы готовой очереди 00056.
		index = "outbox_lane_due_idx"
	case 57:
		index = "outbox_retry_created_idx"
	}
	assertValidIndex(t, pool, index)
	for _, index := range []string{
		"outbox_lane_due_idx",
		"outbox_untaken_dedup_idx",
		"outbox_quarantine_id_idx",
		"outbox_quarantine_kind_id_idx",
	} {
		assertValidIndex(t, pool, index)
	}
	if test.version == 56 {
		var retained uint32
		err := pool.QueryRow(ctx, preservedIndexOID).Scan(&retained)
		if err != nil || retained != preserved {
			t.Fatal("valid-индекс был перестроен", err)
		}
	}
	if test.version == 59 {
		var original, repeated uint32
		if err := pool.QueryRow(ctx, preservedPickupIndexOID, index).Scan(&original); err != nil {
			t.Fatal(err)
		}
		if err := postgres.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
		err := pool.QueryRow(ctx, preservedPickupIndexOID, index).Scan(&repeated)
		if err != nil || original != repeated {
			t.Fatal("повтор перестроил valid-индекс", test.name, original, repeated, err)
		}
	}
	if test.version >= 56 {
		if _, err := pool.Exec(ctx, interruptedOutboxInsert); err != nil {
			t.Fatal(err)
		}
		tasks, err := fake.TasksOf(postgres.NewTaskQueue(postgres.Single(pool)).Claim(ctx, ports.ClaimRequest{
			Now:        time.Now().Add(time.Minute),
			Limit:      1,
			PerTenant:  1,
			LeaseUntil: time.Now().Add(2 * time.Minute),
		}))
		if err != nil || len(tasks) != 1 {
			t.Fatal("очередь не работает", tasks, err)
		}
	}
}

func assertValidIndex(t *testing.T, pool *pgxpool.Pool, name string) {
	t.Helper()
	var valid bool
	err := pool.QueryRow(context.Background(), interruptedIndexValid, name).Scan(&valid)
	if err != nil || !valid {
		t.Fatal("индекс не valid", name, err)
	}
}

func interruptIndexBuild(t *testing.T, pool *pgxpool.Pool, statement, insert, name string) {
	t.Helper()
	ctx := context.Background()
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err := blocker.Exec(ctx, insert); err != nil {
		t.Fatal(err)
	}
	builder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer builder.Release()
	buildCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := builder.Exec(buildCtx, statement)
		finished <- err
	}()
	defer func() {
		cancel()
		if err := <-finished; err == nil {
			t.Error("построение не было прервано")
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var invalid bool
		if err := pool.QueryRow(ctx, indexBuildStarted, name).Scan(&invalid); err != nil {
			t.Fatal(err)
		}
		if invalid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("индекс не начал строиться")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestInterruptedConcurrentRollbackRecovers(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := postgres.MigrateDownTo(ctx, pool, 56); err != nil {
		t.Fatal(err)
	}
	statement := `CREATE INDEX CONCURRENTLY IF NOT EXISTS outbox_due_idx ON outbox (done_at, next_at)`
	interruptIndexBuild(t, pool, statement, interruptedOutboxInsert, "outbox_due_idx")
	if err := postgres.MigrateDown(ctx, pool); err != nil {
		t.Fatal(err)
	}
	assertValidIndex(t, pool, "outbox_due_idx")
	assertValidIndex(t, pool, "outbox_tenant_due_idx")
	if _, err := pool.Exec(ctx, interruptedOutboxInsert); err != nil {
		t.Fatal(err)
	}
}
