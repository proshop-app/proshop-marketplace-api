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
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/referenceapi"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/taxonomyfile"
)

func loadFile(path string) error {
	file, err := os.Open(path) //nolint:gosec // Путь файла явно задаёт локальный оператор.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	definition, err := taxonomyfile.Decode(file)
	if err != nil {
		return err
	}
	database, err := config.LoadDatabase(os.LookupEnv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := postgres.Open(ctx, database.URL, 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	reference, err := config.LoadReferenceReader(os.LookupEnv)
	if err != nil {
		return err
	}
	values := referenceapi.New(referenceapi.Config{
		BaseURL:   reference.Platform.URL,
		ReadToken: reference.Reference.ReadToken,
		Timeout:   reference.Platform.Timeout,
	})
	uow := apptaxonomy.WithReferenceValues(postgres.NewUnitOfWork(pool), values)
	err = apptaxonomy.NewService(uow, clock.System{}).Load(ctx, definition)
	if err == nil {
		fmt.Printf("таксономия: версия %d загружена\n", definition.Version)
	}
	return err
}
