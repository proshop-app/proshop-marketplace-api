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

	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"

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
	if len(args) > 0 && args[0] == "manifest" {
		return manifestCommand(args[1:], output, diagnostics)
	}
	if len(args) == 0 ||
		(args[0] != "check" && args[0] != "apply" && args[0] != "rollback" && args[0] != "remove-hint") {
		return failure(diagnostics, "использование: taxonomy check|apply|rollback|remove-hint", 2)
	}
	options := flag.NewFlagSet("taxonomy", flag.ContinueOnError)
	options.SetOutput(diagnostics)
	expected := options.Int64("expected-version", 0, "версия снимка для явного обновления после правок оператора")
	attribute := options.String("attribute", "", "строковый атрибут для удаления добавленной подсказки")
	dictionary := options.String("dictionary", "", "ожидаемый текущий словарь атрибута")
	previous := options.Int64("previous-version", 0, "сохранённая версия, где атрибут ещё не имел словаря")
	if err := options.Parse(args[1:]); err != nil || options.NArg() != 0 ||
		(args[0] != "apply" && args[0] != "remove-hint" && *expected != 0) {
		return failure(diagnostics, "неверные параметры команды taxonomy", 2)
	}
	remove := app.RemoveHintCommand{
		Attribute:       *attribute,
		Dictionary:      *dictionary,
		PreviousVersion: *previous,
		ExpectedVersion: *expected,
	}
	if !validRemoveHintOptions(args[0], remove) {
		return failure(diagnostics, "неверные параметры удаления подсказки", 2)
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
	moderationConfig, err := config.LoadModeration(os.LookupEnv)
	if err != nil {
		return failure(diagnostics, err.Error(), 2)
	}
	uow := postgres.NewUnitOfWork(pool)
	evaluator := appmoderation.NewService(uow, clock.System{}, random.IDs{}, appmoderation.Config{
		ProbationOffers: moderationConfig.ProbationOffers,
		RequireImage:    moderationConfig.RequireImage,
	})
	service := app.NewService(uow, clock.System{}, evaluator)
	if args[0] == "remove-hint" {
		version, err := service.RemoveHint(ctx, remove)
		if err != nil {
			return failure(diagnostics, "подсказка не удалена: проверьте версию, словарь и историю", 1)
		}
		if err := json.NewEncoder(output).Encode(map[string]int64{"version": version}); err != nil {
			return 1
		}
		return 0
	}
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
