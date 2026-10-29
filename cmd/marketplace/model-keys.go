package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func modelKeysReport(ctx context.Context, cfg config.Config, _ *slog.Logger) error {
	args := os.Args[2:]
	if len(args) == 0 || args[0] != "report" {
		return fmt.Errorf("использование: marketplace model-keys report --out <файл>")
	}
	flags := flag.NewFlagSet("model-keys report", flag.ContinueOnError)
	path := flags.String("out", "", "файл JSON отчёта")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *path == "" || flags.NArg() != 0 {
		return fmt.Errorf("требуется --out <файл>")
	}
	pool, err := postgres.Open(ctx, cfg.Database.URL, cfg.Database.MaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	report, err := postgres.NewModelKeyStats(pool).Report(ctx, true)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "    ")
	if err != nil {
		return err
	}

	return os.WriteFile(*path, append(data, '\n'), 0600)
}
