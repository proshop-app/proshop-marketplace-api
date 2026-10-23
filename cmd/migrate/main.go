// Команда migrate — миграции базы маркетплейса из репозитория.
//
//	migrate [up]   применить все
//	migrate down   откатить последнюю
//	migrate status показать состояние
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

func main() {
	os.Exit(run())
}

func run() int {
	db, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

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

	command := "up"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	switch command {
	case "up":
		err = postgres.Migrate(ctx, pool)
	case "down":
		err = postgres.MigrateDown(ctx, pool)
	case "status":
		err = printStatus(ctx, pool)
	default:
		fmt.Fprintf(os.Stderr, "неизвестная команда %q: up, down или status\n", command)

		return 2
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	return 0
}

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
