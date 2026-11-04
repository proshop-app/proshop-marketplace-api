package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/telemetry"
)

// command — команда процесса.
type command func(ctx context.Context, cfg config.Config, logger *slog.Logger) error

// commands — команды по имени; без имени — serve.
var commands = map[string]command{
	"serve":                 serve,
	"reindex":               reindex,
	"refresh-pickup-points": refreshPickupPoints,
	"reference":             referenceDelivery,
	"model-keys":            modelKeysReport,
}

// run проверяет окружение и выполняет команду до сигнала остановки; результат — код выхода.
func run() int {
	var cfg config.Config
	var err error
	switch {
	case len(os.Args) >= 3 && os.Args[1] == "reference" && os.Args[2] == "check":
	case len(os.Args) >= 3 && os.Args[1] == "reference" && os.Args[2] == "archive-pending":
		cfg.Database, err = config.LoadDatabase(os.LookupEnv)
	case len(os.Args) >= 2 && os.Args[1] == "reference":
		cfg, err = config.LoadReference(os.LookupEnv)
	default:
		cfg, err = config.Load(os.LookupEnv)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 2
	}

	name := "serve"
	if len(os.Args) > 1 {
		name = os.Args[1]
	}

	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "неизвестная команда %q: serve, reindex, refresh-pickup-points или reference\n", name)

		return 2
	}

	logger := telemetry.NewLogger(cfg.LogLevel, os.Stdout)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cmd(ctx, cfg, logger); err != nil {
		logger.Error("процесс завершился с ошибкой", slog.Any("error", err))

		return 1
	}

	return 0
}
