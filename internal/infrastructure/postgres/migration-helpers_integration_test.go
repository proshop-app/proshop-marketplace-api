//go:build integration

package postgres_test

import (
	"context"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Фиксируем целевую версию, чтобы новые миграции не меняли условия проверки старой.
func migrateUpTo(ctx context.Context, pool *pgxpool.Pool, version int64) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS("migrations"))
	if err != nil {
		return err
	}
	_, err = provider.UpTo(ctx, version)
	return err
}
