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
	"использование: marketplace reference check|apply|apply-all|rollback --dir <каталог>; import --file <пакет.json>",
)

// reindex перестраивает индекс предложений из проекции.
func reindex(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := openCommandPool(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()
	index, err := openSearch(cfg)
	if err != nil {
		return err
	}

	uow := postgres.NewUnitOfWork(pool)
	references := appreference.NewService(uow, clock.System{}, random.IDs{}, nil, appreference.Config{})
	models := appcatalog.NewModelKeys(referencecatalog.New(references), nil, cfg.ModelKeys)
	indexer := appcatalog.NewIndexer(
		uow,
		index,
		clock.System{},
		indexBatch,
		postgres.NewIndexRebuildLock(postgres.DirectSessionsOf(pool)),
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

// referenceDelivery принимает каталог поставки или файл пакета для импорта.
// check не подключается к базе; каждое изменение выполняется одной транзакцией.
func referenceDelivery(ctx context.Context, cfg config.Config, _ *slog.Logger) error {
	return runReferenceDelivery(ctx, cfg, os.Args[2:], os.Stdout)
}

func runReferenceDelivery(ctx context.Context, cfg config.Config, args []string, output io.Writer) error {
	if len(args) > 0 && args[0] == "import" {
		return runReferenceImport(ctx, cfg, args[1:], output)
	}
	if len(args) == 0 || (args[0] != "check" && args[0] != "apply" && args[0] != "rollback" && args[0] != "apply-all") {
		return errReferenceUsage
	}
	dir, err := referenceDirectory(args)
	if err != nil {
		return err
	}
	if args[0] == "apply-all" {
		directories, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, directory := range directories {
			if !directory.IsDir() {
				continue
			}
			child := []string{"apply", "--dir", filepath.Join(dir, directory.Name())}
			if err := runReferenceDelivery(ctx, cfg, child, output); err != nil {
				return err
			}
		}
		return nil
	}
	delivery, summary, err := referencedata.Load(os.DirFS(dir))
	if err != nil {
		return err
	}
	if args[0] == "check" {
		return json.NewEncoder(output).Encode(summary)
	}
	pool, err := openCommandPool(ctx, cfg.Database)
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
		return fmt.Errorf("поставка реестра не применена: %s: %s", typed.Key(), typed.Message())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return errors.New("поставка реестра не применена: проверьте миграции и доступность базы")
}

func moderationReevaluate(ctx context.Context, cfg config.Config, _ *slog.Logger) error {
	if len(os.Args) != 3 || os.Args[2] != "reevaluate" {
		return errors.New("использование: marketplace moderation reevaluate")
	}
	pool, err := openCommandPool(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()
	service := appmoderation.NewService(
		postgres.NewUnitOfWork(pool),
		clock.System{},
		random.IDs{},
		appmoderation.Config{
			ProbationOffers: cfg.Moderation.ProbationOffers,
			RequireImage:    cfg.Moderation.RequireImage,
		},
	)
	return postgres.NewIndexRebuildLock(postgres.DirectSessionsOf(pool)).WithLock(ctx, service.EnqueueAll)
}

func referenceDirectory(args []string) (string, error) {
	options := flag.NewFlagSet("reference", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	defaultDir := ""
	if args[0] == "apply-all" {
		defaultDir = "/reference"
	}
	dir := options.String("dir", defaultDir, "каталог с manifest.json и entries.json")
	if err := options.Parse(args[1:]); err != nil || options.NArg() != 0 || *dir == "" {
		return "", errReferenceUsage
	}
	return *dir, nil
}

func runReferenceImport(ctx context.Context, cfg config.Config, args []string, output io.Writer) error {
	options := flag.NewFlagSet("reference import", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	file := options.String("file", "", "файл пакета ImportPackage v1 в JSON")
	if err := options.Parse(args); err != nil || options.NArg() != 0 || *file == "" {
		return errReferenceUsage
	}
	input, err := referencedata.LoadImport(os.DirFS(filepath.Dir(*file)), filepath.Base(*file))
	if err != nil {
		return deliveryFailure(err)
	}
	pool, err := openCommandPool(ctx, cfg.Database)
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
		return deliveryFailure(err)
	}
	return json.NewEncoder(output).Encode(results)
}
