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
	domaintaxonomy "github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/taxonomydata"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const commandTimeout = 2 * time.Minute

type bundleExport struct {
	Release    int64                     `json:"release"`
	Checksum   string                    `json:"checksum"`
	Definition domaintaxonomy.Definition `json:"definition"`
}

type bundleInfo struct {
	ReplaceCategories bool                  `json:"replaceCategories"`
	Release           int64                 `json:"release"`
	Checksum          string                `json:"checksum"`
	Coverage          taxonomydata.Coverage `json:"coverage"`
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
	export := options.Bool("export", false, "выгрузка проверенного развёрнутого определения в JSON")
	review := options.Bool("review", false, "полный отчёт лист ↔ схема по направлениям")
	direction := options.String("direction", "", "направление отчёта")
	confirm := options.Bool("confirm-category-replacement", false, "подтверждение замены категорий")
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
	release.ConfirmCategoryReplacement = *confirm
	if args[0] == "apply" && release.ReplaceCategories && !*confirm {
		return failure(diagnostics, "нужен --confirm-category-replacement: товары требуют переназначения", 2)
	}
	if args[0] != "apply" && *confirm {
		return failure(diagnostics, "подтверждение допустимо только для apply", 2)
	}
	if args[0] != "check" && (*review || *export || *direction != "") ||
		*direction != "" && !*review || *export && *review {
		return failure(diagnostics, "отчёт доступен в check --review", 2)
	}
	if args[0] == "check" {
		if *export {
			value := bundleExport{
				Release:    release.Version,
				Checksum:   release.Checksum,
				Definition: release.Definition(),
			}
			if err := json.NewEncoder(output).Encode(value); err != nil {
				return 1
			}
			return 0
		}
		if *review {
			if err := taxonomydata.WriteReview(output, release, *direction); err != nil {
				return failure(diagnostics, err.Error(), 1)
			}
			return 0
		}
		info := bundleInfo{
			ReplaceCategories: release.ReplaceCategories,
			Release:           release.Version,
			Checksum:          release.Checksum,
			Coverage:          coverage,
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
		return releaseFailure(diagnostics, err)
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

func releaseFailure(output io.Writer, err error) int {
	message := "поставка не применена: проверьте миграции, версию и изменения оператора"
	if domain, ok := errs.As(err); ok && domain.Code() != errs.Internal && domain.Code() != errs.Unavailable {
		// Причина и внешняя обёртка могут содержать реквизиты подключения; выводим только доменное сообщение.
		message = "поставка не применена: " + domain.Key() + ": " + domain.Message()
	}
	return failure(output, message, 1)
}
