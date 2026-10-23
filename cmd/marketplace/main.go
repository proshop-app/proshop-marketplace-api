// Команда marketplace — процесс маркетплейса. Граф зависимостей собирается
// здесь руками: конфиг → хранилища → сценарии приложения → транспорт.
//
//	marketplace          HTTP, консьюмер Kafka, разбор outbox и чистка
//	marketplace reindex  перестроение индекса предложений из проекции
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	infrakafka "github.com/proshop-app/proshop-marketplace/internal/infrastructure/kafka"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/opensearch"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/telemetry"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

// indexBatch — предложений за один проход синхронизации индекса.
const indexBatch = 500

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 2
	}

	logger := telemetry.NewLogger(cfg.LogLevel, os.Stdout)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	switch command {
	case "serve":
		err = serve(ctx, cfg, logger)
	case "reindex":
		err = reindex(ctx, cfg, logger)
	default:
		fmt.Fprintf(os.Stderr, "неизвестная команда %q: serve или reindex\n", command)

		return 2
	}

	if err != nil {
		logger.Error("процесс завершился с ошибкой", slog.Any("error", err))

		return 1
	}

	return 0
}

type storage struct {
	pool  *pgxpool.Pool
	index *opensearch.Index
}

func openStorage(ctx context.Context, cfg config.Config) (*storage, error) {
	pool, err := postgres.Open(ctx, cfg.Database.URL, cfg.Database.MaxConns)
	if err != nil {
		return nil, err
	}

	index, err := opensearch.New(opensearch.Config{
		URLs: cfg.OpenSearch.URLs, Username: cfg.OpenSearch.Username, Password: cfg.OpenSearch.Password,
		InsecureSkipVerify: cfg.OpenSearch.InsecureSkipVerify, Alias: cfg.OpenSearch.Index,
		Shards: cfg.OpenSearch.Shards, Replicas: cfg.OpenSearch.Replicas,
	})
	if err != nil {
		pool.Close()

		return nil, err
	}

	return &storage{pool: pool, index: index}, nil
}

func reindex(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	store, err := openStorage(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.pool.Close()

	indexer := appcatalog.NewIndexer(postgres.NewUnitOfWork(store.pool), store.index, clock.System{}, indexBatch)

	result, err := indexer.Rebuild(ctx)
	if err != nil {
		return fmt.Errorf("перестроение индекса: %w", err)
	}

	logger.Info("индекс перестроен", slog.String("generation", result.Generation),
		slog.Int("indexed", result.Indexed), slog.Int("caughtUp", result.CaughtUp))

	return nil
}

func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	store, err := openStorage(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.pool.Close()

	registry := telemetry.NewRegistry()
	systemClock := clock.System{}
	uow := postgres.NewUnitOfWork(store.pool)

	projector := appcatalog.NewProjector(uow, systemClock)
	indexer := appcatalog.NewIndexer(uow, store.index, systemClock, indexBatch)
	queries := appcatalog.NewQueries(uow, store.index)
	participants := appparticipant.NewService(uow, systemClock)
	tenantRegistry := appparticipant.NewRegistry(uow, systemClock)

	dispatcher := messaging.NewDispatcher(postgres.NewTaskQueue(store.pool), systemClock, messaging.DispatcherConfig{
		Batch: cfg.Outbox.Batch, PerTenant: cfg.Outbox.PerTenant, Lease: cfg.Outbox.Lease,
	}, map[string]messaging.Handler{
		appcatalog.TaskSyncOffer:  indexer.HandleTask,
		appcatalog.TaskSyncTenant: indexer.HandleTask,
	})
	janitor := messaging.NewJanitor(postgres.NewRetention(store.pool), systemClock, messaging.RetentionPolicy{
		ProcessedEvents: cfg.Retention.ProcessedEvents, OutboxDone: cfg.Retention.OutboxDone,
		ParkedEvents: cfg.Retention.ParkedEvents, Batch: 1000,
	})

	built, err := indexer.EnsureIndex(ctx)
	if err != nil {
		return fmt.Errorf("индекс предложений: %w", err)
	}
	if built {
		logger.Info("индекс предложений собран из проекции")
	}

	router := kafka.NewRouter(projector, tenantRegistry)
	topics := kafka.FullTopics(cfg.Kafka.TopicPrefix, router)
	reader := infrakafka.NewReader(infrakafka.Config{
		Brokers: cfg.Kafka.Brokers, GroupID: cfg.Kafka.GroupID, Topics: topics, Logger: logger,
	})
	consumer := kafka.NewConsumer(reader, router, messaging.NewParker(uow), kafka.ConsumerConfig{
		TopicPrefix: cfg.Kafka.TopicPrefix,
		MaxAttempts: cfg.Kafka.MaxAttempts,
		Backoff:     messaging.Backoff,
		Ready:       store.pool.Ping,
		Now:         systemClock.Now,
	}, logger, registry)

	server := &http.Server{
		Addr:              cfg.HTTP.Addr,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		Handler: transport.NewHandler(transport.Deps{
			Catalog:       queries,
			Participants:  participants,
			OperatorToken: cfg.Operator.Token,
			Checks: []transport.Check{
				{Name: "postgres", Probe: store.pool.Ping},
				{Name: "opensearch", Probe: store.index.Ping},
				{Name: "kafka", Probe: func(ctx context.Context) error { return infrakafka.Ping(ctx, cfg.Kafka.Brokers) }},
			},
			Metrics:    promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
			Registerer: registry,
			Logger:     logger,
		}),
	}

	group, groupCtx := errgroup.WithContext(ctx)

	group.Go(func() error {
		logger.Info("HTTP слушает", slog.String("addr", cfg.HTTP.Addr))

		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP: %w", err)
		}

		return nil
	})
	group.Go(func() error {
		<-groupCtx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(groupCtx), cfg.HTTP.ShutdownTimeout)
		defer cancel()

		return server.Shutdown(shutdownCtx)
	})
	group.Go(func() error {
		err := consumer.Run(groupCtx)

		if closeErr := reader.Close(); closeErr != nil {
			logger.Warn("kafka: читатель закрылся с ошибкой", slog.Any("error", closeErr))
		}

		return err
	})
	group.Go(func() error {
		return jobs.NewOutboxRunner(dispatcher, cfg.Outbox.PollInterval, cfg.Outbox.Batch, logger, registry).Run(groupCtx)
	})
	group.Go(func() error {
		return jobs.NewRetentionRunner(janitor, cfg.Retention.Interval, logger).Run(groupCtx)
	})

	logger.Info("маркетплейс запущен", slog.String("group", cfg.Kafka.GroupID), slog.Any("topics", topics))

	return group.Wait()
}
