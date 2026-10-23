package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

// command — действие с миграциями.
type command func(ctx context.Context, pool *pgxpool.Pool) error

// commands — действия по имени; без имени — up.
var commands = map[string]command{
	"up":     postgres.Migrate,
	"down":   postgres.MigrateDown,
	"status": printStatus,
}

// run подключается к базе из DATABASE_URL и выполняет действие; результат — код выхода.
func run() int {
	db, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 2
	}

	name := "up"
	if len(os.Args) > 1 {
		name = os.Args[1]
	}

	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "неизвестная команда %q: up, down или status\n", name)

		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.Open(ctx, db.URL, 2)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}
	defer pool.Close()

	if err := cmd(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	return 0
}

// printStatus печатает применённые и ожидающие миграции.
func printStatus(ctx context.Context, pool *pgxpool.Pool) error {
	statuses, err := postgres.MigrationStatus(ctx, pool)
	if err != nil {
		return err
	}

	for _, status := range statuses {
		fmt.Printf("%-8s %s\n", status.State, status.Source.Path)
	}

	return nil
}
