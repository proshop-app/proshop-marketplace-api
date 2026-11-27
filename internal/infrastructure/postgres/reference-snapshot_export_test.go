//go:build integration

package postgres

const (
	ExplainSnapshotEntries = "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) " + selectSnapshotEntries
	ExplainSnapshotChanges = "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) " + selectSnapshotChanges
)
