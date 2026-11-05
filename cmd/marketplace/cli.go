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
	"moderation":            moderationReevaluate,
	"serve":                 serve,
	"reindex":               reindex,
	"refresh-pickup-points": refreshPickupPoints,
	"reference":             referenceDelivery,
	"model-keys":            modelKeysReport,
}

// run проверяет окружение и выполняет команду до сигнала остановки; результат — код выхода.
func run() int {
	name := "serve"
	if len(os.Args) > 1 {
		name = os.Args[1]
	}
	cfg, err := commandConfig(name, os.LookupEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
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

// Поставка справочников работает только с PostgreSQL; секреты HTTP и Kafka ей не нужны.
func commandConfig(name string, env func(string) (string, bool)) (config.Config, error) {
	if name == "reference" {
		database, err := config.LoadDatabase(env)
		return config.Config{Database: database, LogLevel: "info"}, err
	}
	return config.Load(env)
}
