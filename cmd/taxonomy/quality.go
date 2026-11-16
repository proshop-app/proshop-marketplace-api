package main

import (
	"context"
	"io"
	"os"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/domain/colorpalette"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/taxonomydata"
)

func qualityCommand(output, diagnostics io.Writer) int {
	database, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		return failure(diagnostics, "не задан корректный DATABASE_URL", 2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	pool, err := postgres.Open(ctx, database.URL, 2)
	if err != nil {
		return failure(diagnostics, "не удалось подключиться к PostgreSQL", 1)
	}
	defer pool.Close()
	err = postgres.NewUnitOfWork(pool).Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		entries, err := tx.Reference().AllEntries(ctx, colorpalette.Dictionary)
		if err != nil {
			return err
		}
		return taxonomydata.WriteQuality(output, entries)
	})
	if err != nil {
		return failure(diagnostics, err.Error(), 1)
	}
	return 0
}
