package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"
)

type migrationDirection int

const (
	migrationRead migrationDirection = iota
	migrationUp
	migrationDown
)

const (
	migrationVersionTableExists   = `SELECT to_regclass('goose_db_version') IS NOT NULL`
	selectInvalidMigrationIndexes = `SELECT n.nspname, c.relname
        FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = current_schema() AND c.relname = ANY($1::text[]) AND NOT i.indisvalid`
	dropInvalidMigrationIndex = `DROP INDEX CONCURRENTLY IF EXISTS `
)

// Восстанавливаем только DDL нужного направления: valid-индексы не перестраиваем.
var retryableMigrationIndexes = map[int64][]string{
	65: {
		"catalog_recommendations_category",
		"catalog_recommendations_popular",
		"catalog_recommendations_tenant_rank",
	},
	62: {
		"moderation_case_probation",
		"moderation_case_tenant_open",
		"moderation_case_open_id",
		"moderation_case_source_open_id",
		"moderation_case_open_reasons",
	},
	59: {"participants_pickup_cdek", "participants_pickup_yandex"},
	54: {
		"moderation_cases_offer_history",
		"moderation_decisions_case_history",
		"promotion_decisions_campaign_history",
		"promotion_campaigns_storefront",
		"promotion_campaigns_tick",
		"promotion_campaigns_offer_read",
		"catalog_products_store_summary",
	},
	55: {"parked_events_resolved_updated_idx"},
	56: {
		"outbox_pending_due_idx",
		"outbox_pending_tenant_due_idx",
		"outbox_quarantine_id_idx",
		"outbox_quarantine_kind_id_idx",
	},
	57: {"outbox_retry_kind_created_idx"},
}

var rollbackMigrationIndexes = map[int64][]string{
	56: {"outbox_due_idx", "outbox_tenant_due_idx"},
}

type repairingMigrationLocker struct {
	lock.SessionLocker
	direction migrationDirection
}

func migrationLocker(direction migrationDirection) (lock.SessionLocker, error) {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil || direction == migrationRead {
		return locker, err
	}
	return &repairingMigrationLocker{
		SessionLocker: locker,
		direction:     direction,
	}, nil
}

// SessionLock восстанавливает незавершённый DDL на той же сессии под замком goose.
// В DO/транзакции PostgreSQL не разрешает условный DROP INDEX CONCURRENTLY.
func (l *repairingMigrationLocker) SessionLock(ctx context.Context, conn *sql.Conn) error {
	if err := l.SessionLocker.SessionLock(ctx, conn); err != nil {
		return err
	}
	if err := repairMigrationIndexes(ctx, conn, l.direction); err != nil {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return errors.Join(err, l.SessionUnlock(unlockCtx, conn))
	}
	return nil
}

func repairMigrationIndexes(ctx context.Context, conn *sql.Conn, direction migrationDirection) error {
	var exists bool
	if err := conn.QueryRowContext(ctx, migrationVersionTableExists).Scan(&exists); err != nil || !exists {
		return err
	}
	store, err := database.NewStore(database.DialectPostgres, goose.DefaultTablename)
	if err != nil {
		return err
	}
	candidates := retryableMigrationIndexes
	if direction == migrationDown {
		candidates = rollbackMigrationIndexes
	}
	var names []string
	for version, indexes := range candidates {
		_, err := store.GetMigration(ctx, conn, version)
		if err != nil && !errors.Is(err, database.ErrVersionNotFound) {
			return err
		}
		applied := err == nil
		if applied == (direction == migrationDown) {
			names = append(names, indexes...)
		}
	}
	if len(names) == 0 {
		return nil
	}
	rows, err := conn.QueryContext(ctx, selectInvalidMigrationIndexes, names)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var broken []pgx.Identifier
	for rows.Next() {
		var schema, name string
		if err := rows.Scan(&schema, &name); err != nil {
			return err
		}
		broken = append(broken, pgx.Identifier{schema, name})
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, index := range broken {
		if _, err := conn.ExecContext(ctx, dropInvalidMigrationIndex+index.Sanitize()); err != nil {
			return err
		}
	}
	return nil
}
