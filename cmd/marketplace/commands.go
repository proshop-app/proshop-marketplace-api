package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/referenceapi"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/referencedata"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/telemetry"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
)

// errReferenceUsage — команда поставки реестра вызвана без действия или каталога.
var errReferenceUsage = errors.New("использование: marketplace reference check|apply|rollback --dir <каталог поставки>")

// reindex перестраивает индекс предложений из проекции.
func reindex(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	store, err := openStorage(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.close()

	uow := postgres.NewUnitOfWork(store.pool)
	references := referenceapi.New(referenceConfig(cfg))
	models := appcatalog.NewModelKeys(references, nil, cfg.ModelKeys)
	indexer := appcatalog.NewIndexer(
		uow,
		store.index,
		clock.System{},
		indexBatch,
		postgres.NewIndexRebuildLock(store.pool),
		models,
	)

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

// referenceDelivery проверяет, применяет или откатывает поставку реестра из
// каталога. check не подключается к базе; apply и rollback — одной транзакцией.
func referenceDelivery(ctx context.Context, cfg config.Config, _ *slog.Logger) error {
	return runReferenceDelivery(ctx, cfg, os.Args[2:], os.Stdout)
}

func runReferenceDelivery(ctx context.Context, cfg config.Config, args []string, output io.Writer) error {
	if len(args) > 0 && args[0] == "archive-pending" {
		return runReferenceArchive(ctx, cfg, args[1:], output)
	}
	if len(args) == 0 || (args[0] != "check" && args[0] != "apply" && args[0] != "rollback") {
		return errReferenceUsage
	}
	options := flag.NewFlagSet("reference", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	dir := options.String("dir", "", "каталог с manifest.json и entries.json")
	if err := options.Parse(args[1:]); err != nil || options.NArg() != 0 || *dir == "" {
		return errReferenceUsage
	}
	delivery, summary, err := referencedata.Load(os.DirFS(*dir))
	if err != nil {
		return err
	}
	if args[0] == "check" {
		return json.NewEncoder(output).Encode(summary)
	}
	api := referenceapi.New(referenceConfig(cfg))
	body, err := json.Marshal(delivery)
	if err != nil {
		return err
	}
	response, err := api.Do(ctx, ports.ReferenceRequest{
		Method:     http.MethodPost,
		Dictionary: delivery.Dictionary,
		Resource:   "deliveries/" + args[0],
		Body:       body,
		Actor:      "cli",
		Write:      true,
	})
	if err != nil {
		return err
	}
	if response.Status != http.StatusOK {
		return fmt.Errorf("поставка реестра не применена: HTTP %d", response.Status)
	}
	_, err = fmt.Fprintln(output, string(response.Body))
	return err
}

func referenceConfig(cfg config.Config) referenceapi.Config {
	return referenceapi.Config{
		BaseURL:    cfg.Platform.URL,
		ReadToken:  cfg.Reference.ReadToken,
		WriteToken: cfg.Reference.WriteToken,
		Timeout:    cfg.Platform.Timeout,
	}
}
