// Команда geoip доставляет опубликованную базу отдельно от HTTP-сервиса.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/localgeoip"
)

func run(args []string) int {
	flags := flag.NewFlagSet("geoip", flag.ContinueOnError)
	path := flags.String("file", "/geoip/dbip-city-lite.mmdb", "путь действующего MMDB на постоянном томе")
	month := flags.String("month", time.Now().UTC().Format("2006-01"), "месячная поставка DB-IP YYYY-MM")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := localgeoip.NewUpdater().Update(ctx, *path, *month); err != nil {
		logger.Error("обновление DB-IP не завершено; прежний файл сохранён", slog.Any("error", err))
		return 1
	}
	logger.Info("DB-IP обновлена атомарно", slog.String("month", *month))
	return 0
}
