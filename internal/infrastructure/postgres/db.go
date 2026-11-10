// Package postgres — хранилище маркетплейса в PostgreSQL через GORM: миграции,
// UnitOfWork поверх транзакции и репозитории.
package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"gorm.io/gorm"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const selectDeliveryMigrationReport = `SELECT tenant_id, previous_shop_enabled, previous_status
 FROM operator_delivery_migration_report ORDER BY tenant_id`

//go:embed migrations/*.sql
var migrations embed.FS

// ErrUnavailable — база недоступна: соединение, начало или фиксация транзакции.
var ErrUnavailable = errs.New(errs.Unavailable, "storage.unavailable", "хранилище недоступно")

// Open — пул соединений с проверкой связи.
func Open(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("адрес базы: %w", err)
	}

	cfg.MaxConns = maxConns

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, ErrUnavailable.Wrap(err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()

		return nil, ErrUnavailable.Wrap(err)
	}

	return pool, nil
}

// Migrate применяет миграции из репозитория до последней версии.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return runGoose(ctx, pool, func(provider *goose.Provider) error {
		results, err := provider.Up(ctx)
		if err != nil {
			return err
		}
		for _, result := range results {
			if result.Source.Version == 22 {
				return logDeliveryMigrationReport(ctx, pool)
			}
		}
		return nil
	})
}

// MigrationStatus — применённые и ожидающие миграции.
func MigrationStatus(ctx context.Context, pool *pgxpool.Pool) ([]*goose.MigrationStatus, error) {
	var statuses []*goose.MigrationStatus

	err := runGoose(ctx, pool, func(provider *goose.Provider) error {
		var err error
		statuses, err = provider.Status(ctx)

		return err
	})

	return statuses, err
}

// MigrateDown откатывает последнюю миграцию.
func MigrateDown(ctx context.Context, pool *pgxpool.Pool) error {
	return runGoose(ctx, pool, func(provider *goose.Provider) error {
		_, err := provider.Down(ctx)

		return err
	})
}

// MigrateDownTo откатывает миграции новее указанной версии, сохраняя её применённой.
func MigrateDownTo(ctx context.Context, pool *pgxpool.Pool, version int64) error {
	return runGoose(ctx, pool, func(provider *goose.Provider) error {
		_, err := provider.DownTo(ctx, version)

		return err
	})
}

func runGoose(ctx context.Context, pool *pgxpool.Pool, run func(*goose.Provider) error) error {
	// закрытие *sql.DB поверх пула не закрывает сам пул
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	if err := db.PingContext(ctx); err != nil {
		return ErrUnavailable.Wrap(err)
	}

	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("миграции: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, files)
	if err != nil {
		return fmt.Errorf("миграции: %w", err)
	}

	if err := run(provider); err != nil {
		return fmt.Errorf("миграции: %w", err)
	}

	return nil
}

// logDeliveryMigrationReport читает сохранённый отчёт после фиксации миграции.
func logDeliveryMigrationReport(ctx context.Context, pool *pgxpool.Pool) error {
	return newConnectionSource(pool).withConnection(ctx, func(db *gorm.DB) error {
		return readDeliveryMigrationReport(ctx, db)
	})
}

func readDeliveryMigrationReport(ctx context.Context, db *gorm.DB) error {
	rows, err := rawSQL(ctx, db, selectDeliveryMigrationReport).Rows()
	if err != nil {
		return fmt.Errorf("отчёт миграции доставки: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var tenant, status string
		var enabled bool
		if err := rows.Scan(&tenant, &enabled, &status); err != nil {
			return fmt.Errorf("участник отчёта миграции: %w", err)
		}
		slog.WarnContext(
			ctx,
			"миграция 22: участие выключено из-за непереносимых зон доставки",
			"tenantId",
			tenant,
			"previousShopEnabled",
			enabled,
			"previousStatus",
			status,
		)
	}
	return rows.Err()
}
