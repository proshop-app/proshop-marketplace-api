package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/referencecatalog"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/referencedata"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/telemetry"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
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
	references := appreference.NewService(uow, clock.System{}, random.IDs{}, nil, appreference.Config{})
	models := appcatalog.NewModelKeys(referencecatalog.New(references), nil, cfg.ModelKeys)
	indexer := appcatalog.NewIndexer(
		postgres.NewUnitOfWork(store.pool),
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
	pool, err := postgres.Open(ctx, cfg.Database.URL, 2)
	if err != nil {
		return errors.New("не удалось подключиться к PostgreSQL")
	}
	defer pool.Close()
	// событие уходит через outbox: публикует его процесс serve
	uow := postgres.NewUnitOfWork(pool)
	service := appreference.NewService(uow, clock.System{}, random.IDs{}, nil, appreference.Config{})
	operation := service.Apply
	if args[0] == "rollback" {
		operation = service.Rollback
	}
	result, err := operation(ctx, delivery)
	if err != nil {
		return deliveryFailure(err)
	}
	return json.NewEncoder(output).Encode(result)
}

// deliveryFailure — причина отказа без текста драйвера: он может содержать адрес базы.
func deliveryFailure(err error) error {
	if typed, ok := errs.As(err); ok && typed.Code() != errs.Internal && typed.Code() != errs.Unavailable {
		return fmt.Errorf("поставка реестра не применена: %s", typed.Message())
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	return errors.New("поставка реестра не применена: проверьте миграции и доступность базы")
}
