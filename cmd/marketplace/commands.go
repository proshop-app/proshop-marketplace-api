package main

import (
	"context"
	"fmt"
	"log/slog"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/telemetry"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
)

// reindex перестраивает индекс предложений из проекции.
func reindex(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	store, err := openStorage(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.close()

	indexer := appcatalog.NewIndexer(postgres.NewUnitOfWork(store.pool), store.index, clock.System{}, indexBatch)

	result, err := indexer.Rebuild(ctx)
	if err != nil {
		return fmt.Errorf("перестроение индекса: %w", err)
	}

	logger.Info(
		"индекс перестроен",
		slog.String("generation", result.Generation),
		slog.Int("indexed", result.Indexed),
		slog.Int("caughtUp", result.CaughtUp),
	)

	return nil
}

// refreshPickupPoints обновляет справочники пунктов выдачи один раз.
func refreshPickupPoints(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	store, err := openStorage(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.close()

	pickups := directory(store, cfg)
	if err := pickups.EnsureIndex(ctx); err != nil {
		return fmt.Errorf("индекс пунктов выдачи: %w", err)
	}

	runner := jobs.NewPickupRefreshRunner(pickups, cfg.Delivery.RefreshInterval, logger, telemetry.NewRegistry())
	runner.RunOnce(ctx)

	return nil
}
