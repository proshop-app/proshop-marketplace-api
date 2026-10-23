// Package postgres — хранилище маркетплейса в PostgreSQL на pgx: миграции,
// UnitOfWork поверх транзакции и репозитории.
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrUnavailable — база недоступна: соединение, начало или фиксация транзакции.
var ErrUnavailable = errs.New(errs.Unavailable, "storage.unavailable", "хранилище недоступно")

const (
	codeUniqueViolation     = "23505"
	codeForeignKeyViolation = "23503"
)

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
		_, err := provider.Up(ctx)

		return err
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

func isUnique(err error, constraint string) bool {
	var pgErr *pgconn.PgError

	return errors.As(err, &pgErr) && pgErr.Code == codeUniqueViolation && (constraint == "" || pgErr.ConstraintName == constraint)
}

func isForeignKey(err error) bool {
	var pgErr *pgconn.PgError

	return errors.As(err, &pgErr) && pgErr.Code == codeForeignKeyViolation
}

func notFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
