package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"

	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
)

const projectionRecoveryCommand = "recover-projection-review"

var (
	errProjectionRecoveryUsage = errors.New("восстановление требует точных версий и SHA двух retained событий")
	errProjectionRecoveryProof = errors.New("некорректное доказательство неизменности исходного товара")
)

type projectionRecoveryExecutor interface {
	RecoverProjection(
		context.Context,
		appmoderation.RecoverProjectionCommand,
	) (appmoderation.RecoverProjectionResult, error)
}

type projectionRecoveryConnector func(context.Context, config.Config) (projectionRecoveryExecutor, func(), error)

// Команда принимает только проверенные полные wire bytes через stdin. Без
// --apply сценарий проверяет CAS и прежнее решение, но не сохраняет изменения.
func recoverProjectionReview(ctx context.Context, cfg config.Config, _ *slog.Logger) error {
	return runProjectionRecovery(ctx, cfg, os.Args[2:], os.Stdin, os.Stdout, openProjectionRecovery)
}

func runProjectionRecovery(
	ctx context.Context,
	cfg config.Config,
	args []string,
	input io.Reader,
	output io.Writer,
	connect projectionRecoveryConnector,
) error {
	options, err := projectionRecoveryOptionsFrom(args)
	if err != nil {
		return err
	}
	command, err := readProjectionRecoveryProof(input, options, clock.System{}.Now())
	if err != nil {
		return err
	}
	service, closeConnection, err := connect(ctx, cfg)
	if err != nil {
		return errors.New("не удалось подключиться к PostgreSQL для восстановления допуска")
	}
	defer closeConnection()
	result, err := service.RecoverProjection(ctx, command)
	if err != nil {
		return projectionRecoveryFailure(err)
	}

	return json.NewEncoder(output).Encode(result)
}

func openProjectionRecovery(
	ctx context.Context,
	cfg config.Config,
) (projectionRecoveryExecutor, func(), error) {
	pool, err := postgres.Open(ctx, cfg.Database.URL, 2)
	if err != nil {
		return nil, nil, err
	}
	service := appmoderation.NewService(
		postgres.NewUnitOfWork(pool),
		clock.System{},
		random.IDs{},
		appmoderation.Config{
			ProbationOffers: cfg.Moderation.ProbationOffers,
			RequireImage:    cfg.Moderation.RequireImage,
		},
	)

	return service, pool.Close, nil
}
