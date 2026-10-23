// Команда taxonomy загружает проверенный файл оператора в проекцию площадки.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/taxonomyfile"
)

func run() int {
	if len(os.Args) != 3 || os.Args[1] != "load" {
		fmt.Fprintln(os.Stderr, "использование: taxonomy load taxonomy/file.yaml")
		return 2
	}
	if err := load(os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func load(path string) error {
	// Путь явно передаёт оператор в CLI; это не значение из сетевого запроса.
	file, err := os.Open(path) //nolint:gosec // G304: локальный файл оператора
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	d, err := taxonomyfile.Decode(file)
	if err != nil {
		return err
	}
	db, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := postgres.Open(ctx, db.URL, 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	err = apptaxonomy.NewService(postgres.NewUnitOfWork(pool), clock.System{}).Load(ctx, d)
	if err == nil {
		fmt.Printf("таксономия: версия %d загружена\n", d.Version)
	}
	return err
}
