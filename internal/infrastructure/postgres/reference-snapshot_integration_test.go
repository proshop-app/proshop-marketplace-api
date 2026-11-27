//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
)

const vacuumReferenceVersions = "VACUUM (ANALYZE) reference.snapshot_versions"

type snapshotPlan struct {
	NodeType      string         `json:"Node Type"`
	IndexName     string         `json:"Index Name"`
	IndexCond     string         `json:"Index Cond"`
	OrderBy       string         `json:"Order By"`
	Rows          int            `json:"Actual Rows"`
	RowsRemoved   int            `json:"Rows Removed by Filter"`
	RowsRechecked int            `json:"Rows Removed by Index Recheck"`
	SharedHits    int            `json:"Shared Hit Blocks"`
	SharedReads   int            `json:"Shared Read Blocks"`
	Plans         []snapshotPlan `json:"Plans"`
}

func checkSnapshotPlan(t *testing.T, pool *pgxpool.Pool, wantRows, maxBlocks int, query string, args ...any) {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	t.Log(string(raw))
	var result []struct {
		Plan snapshotPlan
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result) != 1 {
		t.Fatalf("EXPLAIN: %v", err)
	}
	plan := result[0].Plan
	if plan.Rows != wantRows || plan.SharedHits+plan.SharedReads > maxBlocks || len(plan.Plans) != 1 {
		t.Fatalf("неограниченное чтение страницы: %+v", plan)
	}
	index := plan.Plans[0]
	if index.NodeType != "Index Scan" || index.IndexName == "" || index.IndexCond == "" || index.OrderBy == "" ||
		index.RowsRemoved != 0 || index.RowsRechecked != 0 {
		t.Fatalf("ожидался упорядоченный обход индекса без фильтрации истории: %+v", index)
	}
}

func assertEmptySnapshotAfterRollback(t *testing.T, base *pgxpool.Pool, clock *referenceClock, revision int64) {
	t.Helper()
	ctx := context.Background()
	config := base.Config().Copy()
	counter := &statementCounter{}
	config.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	service := appreference.NewService(postgres.NewUnitOfWork(pool), clock, random.IDs{}, nil, appreference.Config{})
	snapshot, err := service.Snapshot(ctx, referenceDict, nil, "")
	if err != nil || len(snapshot.Entries) != 0 || snapshot.NextCursor != "" || snapshot.Revision != revision {
		t.Fatal(snapshot, err)
	}
	// До VACUUM открываем транзакцию, читаем только строку словаря и фиксируем.
	if got := counter.statements.Load(); got != 3 {
		t.Fatalf("пустой снимок: %d SQL, ожидалось 3 с BEGIN и COMMIT", got)
	}
}

func checkDeliverySnapshotPlans(t *testing.T, pool *pgxpool.Pool, before int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), vacuumReferenceVersions); err != nil {
		t.Fatal(err)
	}
	// Полная и дельта на закреплённой ревизии читают первые 129 образов в индексном порядке.
	checkSnapshotPlan(t, pool, 129, 250, postgres.ExplainSnapshotEntries, referenceDict, before, 0, 129)
	checkSnapshotPlan(t, pool, 129, 250, postgres.ExplainSnapshotChanges, referenceDict, 0, before, 0, 129)
}
