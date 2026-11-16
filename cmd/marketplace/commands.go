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
	"path/filepath"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/referencecatalog"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/referencedata"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// errReferenceUsage — команда реестра вызвана без действия или пути к данным.
var errReferenceUsage = errors.New(
	"использование: marketplace reference import --file <пакет.json>",
)

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
		cfg.Settings.StoreCardRefreshDelay,
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

// referenceCommand импортирует пакет одной транзакцией.
func referenceCommand(ctx context.Context, cfg config.Config, _ *slog.Logger) error {
	return runReferenceCommand(ctx, cfg, os.Args[2:], os.Stdout)
}

func runReferenceCommand(ctx context.Context, cfg config.Config, args []string, output io.Writer) error {
	if len(args) == 0 || args[0] != "import" {
		return errReferenceUsage
	}
	return runReferenceImport(ctx, cfg, args[1:], output)
}

// referenceImportFailure — причина отказа без текста драйвера: он может содержать адрес базы.
func referenceImportFailure(err error) error {
	if typed, ok := errs.As(err); ok && typed.Code() != errs.Internal && typed.Code() != errs.Unavailable {
		return fmt.Errorf("пакет справочников не импортирован: %s: %s", typed.Key(), typed.Message())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return errors.New("пакет справочников не импортирован: проверьте миграции и доступность базы")
}

func moderationReevaluate(ctx context.Context, cfg config.Config, _ *slog.Logger) error {
	if len(os.Args) != 3 || os.Args[2] != "reevaluate" {
		return errors.New("использование: marketplace moderation reevaluate")
	}
	store, err := openStorage(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.close()
	service := appmoderation.NewService(
		postgres.NewUnitOfWork(store.pool),
		clock.System{},
		random.IDs{},
		appmoderation.Config{
			ProbationOffers: cfg.Moderation.ProbationOffers,
			RequireImage:    cfg.Moderation.RequireImage,
		},
	)
	return postgres.NewIndexRebuildLock(store.pool).WithLock(ctx, service.EnqueueAll)
}

func runReferenceImport(ctx context.Context, cfg config.Config, args []string, output io.Writer) error {
	options := flag.NewFlagSet("reference import", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	file := options.String("file", "", "файл пакета ImportPackage v2 в JSON")
	if err := options.Parse(args); err != nil || options.NArg() != 0 || *file == "" {
		return errReferenceUsage
	}
	input, err := referencedata.LoadImport(os.DirFS(filepath.Dir(*file)), filepath.Base(*file))
	if err != nil {
		return referenceImportFailure(err)
	}
	pool, err := postgres.Open(ctx, cfg.Database.URL, 2)
	if err != nil {
		return errors.New("не удалось подключиться к PostgreSQL")
	}
	defer pool.Close()
	service := appreference.NewService(
		postgres.NewUnitOfWork(pool),
		clock.System{},
		random.IDs{},
		nil,
		appreference.Config{},
	)
	results, err := service.Import(ctx, "", input)
	if err != nil {
		return referenceImportFailure(err)
	}
	return json.NewEncoder(output).Encode(results)
}
