// Команда taxonomy проверяет и загружает готовый каталог без запуска API и брокеров.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	app "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/taxonomydata"
)

const commandTimeout = 2 * time.Minute

type bundleInfo struct {
	Release  int64                 `json:"release"`
	Checksum string                `json:"checksum"`
	Coverage taxonomydata.Coverage `json:"coverage"`
}

func run(args []string, output, diagnostics io.Writer) int {
	if len(args) == 2 && args[0] == "load" {
		if err := loadFile(args[1]); err != nil {
			return failure(diagnostics, err.Error(), 1)
		}
		return 0
	}
	if len(args) == 0 || (args[0] != "check" && args[0] != "apply" && args[0] != "rollback") {
		return failure(diagnostics, "использование: taxonomy check|apply|rollback", 2)
	}
	options := flag.NewFlagSet("taxonomy", flag.ContinueOnError)
	options.SetOutput(diagnostics)
	expected := options.Int64("expected-version", 0, "версия снимка для явного обновления после правок оператора")
	if err := options.Parse(args[1:]); err != nil || options.NArg() != 0 ||
		(args[0] != "apply" && *expected != 0) {
		return failure(diagnostics, "неверные параметры команды taxonomy", 2)
	}
	release, coverage, err := taxonomydata.Load()
	if err != nil {
		return failure(diagnostics, err.Error(), 1)
	}
	release.ExpectedVersion = *expected
	if args[0] == "check" {
		info := bundleInfo{
			Release:  release.Version,
			Checksum: release.Checksum,
			Coverage: coverage,
		}
		if err := json.NewEncoder(output).Encode(info); err != nil {
			return 1
		}

		return 0
	}
	database, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		return failure(diagnostics, "не задан корректный DATABASE_URL", 2)
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, commandTimeout)
	defer cancel()
	pool, err := postgres.Open(ctx, database.URL, 2)
	if err != nil {
		return failure(diagnostics, "не удалось подключиться к PostgreSQL", 1)
	}
	defer pool.Close()
	service := app.NewService(postgres.NewUnitOfWork(pool), clock.System{})
	operation := service.Apply
	if args[0] == "rollback" {
		operation = service.Rollback
	}
	result, err := operation(ctx, release)
	if err != nil {
		// Ошибка драйвера может содержать адрес или credentials: CLI не печатает её.
		return failure(diagnostics, "поставка не применена: проверьте миграции, версию и изменения оператора", 1)
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return 1
	}

	return 0
}

func failure(output io.Writer, message string, code int) int {
	if _, err := fmt.Fprintln(output, message); err != nil {
		return 1
	}

	return code
}
