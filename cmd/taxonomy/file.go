package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"

	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
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
	moderationConfig, err := config.LoadModeration(os.LookupEnv)
	if err != nil {
		return err
	}
	uow := postgres.NewUnitOfWork(pool)
	evaluator := appmoderation.NewService(uow, clock.System{}, random.IDs{}, appmoderation.Config{
		ProbationOffers: moderationConfig.ProbationOffers,
		RequireImage:    moderationConfig.RequireImage,
	})
	err = apptaxonomy.NewService(uow, clock.System{}, evaluator).Load(ctx, definition)
	if err == nil {
		fmt.Printf("таксономия: версия %d загружена\n", definition.Version)
	}
	return err
}
