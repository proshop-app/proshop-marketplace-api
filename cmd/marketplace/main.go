// Команда marketplace — процесс маркетплейса. Граф зависимостей собирается
// здесь руками: конфиг → хранилища → сценарии приложения → транспорт.
//
//	marketplace                        HTTP, консьюмер Kafka, разбор outbox и фоновые задачи
//	marketplace reindex                перестроение индекса предложений из проекции
//	marketplace refresh-pickup-points  обновление справочников пунктов выдачи
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

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/cdek"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	infrakafka "github.com/proshop-app/proshop-marketplace/internal/infrastructure/kafka"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/opensearch"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/platformapi"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/smsru"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/telemetry"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/yandex"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

const (
	// indexBatch — предложений за один проход синхронизации индекса.
	indexBatch = 500
	// keyPrefix — префикс ключей маркетплейса в Redis.
	keyPrefix = "marketplace:"
)

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
	case "refresh-pickup-points":
		err = refreshPickupPoints(ctx, cfg, logger)
	default:
		fmt.Fprintf(os.Stderr, "неизвестная команда %q: serve, reindex или refresh-pickup-points\n", command)

		return 2
	}

	if err != nil {
		logger.Error("процесс завершился с ошибкой", slog.Any("error", err))

		return 1
	}

	return 0
}

type storage struct {
	pool    *pgxpool.Pool
	index   *opensearch.Index
	pickups *opensearch.PickupIndex
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

	return &storage{pool: pool, index: index, pickups: opensearch.NewPickupIndex(index, cfg.Delivery.PickupIndex)}, nil
}

// carriers — перевозчики, для которых в окружении есть учётная запись платформы.
func carriers(cfg config.Config) []ports.PickupPointsProvider {
	var providers []ports.PickupPointsProvider

	if c := cfg.Delivery.CDEK; c.ClientID != "" {
		providers = append(providers, cdek.New(cdek.Config{BaseURL: c.URL, ClientID: c.ClientID, ClientSecret: c.ClientSecret}))
	}
	if y := cfg.Delivery.Yandex; y.Token != "" {
		providers = append(providers, yandex.New(yandex.Config{BaseURL: y.URL, Token: y.Token}))
	}

	return providers
}

// holder — имя процесса для аренды фоновых задач.
func holder() string {
	host, err := os.Hostname()
	if err != nil {
		host = "marketplace"
	}

	return fmt.Sprintf("%s:%d", host, os.Getpid())
}

func directoryOf(store *storage, cfg config.Config) *appdelivery.Directory {
	return appdelivery.NewDirectory(postgres.NewUnitOfWork(store.pool), carriers(cfg), store.pickups, postgres.NewLeases(store.pool), clock.System{}, holder())
}

func refreshPickupPoints(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	store, err := openStorage(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.pool.Close()

	directory := directoryOf(store, cfg)
	if err := directory.EnsureIndex(ctx); err != nil {
		return fmt.Errorf("индекс пунктов выдачи: %w", err)
	}

	jobs.NewPickupRefreshRunner(directory, cfg.Delivery.RefreshInterval, logger, telemetry.NewRegistry()).RunOnce(ctx)

	return nil
}

// callCheck — шлюз проверки звонком: живой sms.ru или песочница для разработки.
func callCheck(ctx context.Context, cfg config.Config, logger *slog.Logger) ports.CallCheck {
	if cfg.SmsRu.Mode == config.SmsRuSandbox {
		logger.Warn("вход по звонку в песочнице: любой номер подтверждается сам, только для разработки",
			slog.Duration("confirmAfter", cfg.SmsRu.SandboxConfirmAfter))

		return smsru.NewSandbox(cfg.SmsRu.SandboxConfirmAfter)
	}

	client := smsru.New(smsru.Config{BaseURL: cfg.SmsRu.BaseURL, APIID: cfg.SmsRu.APIID})

	if cfg.SmsRu.CallbackURL != "" {
		if err := client.RegisterCallback(ctx, cfg.SmsRu.CallbackURL); err != nil {
			// без оповещений состояние звонка узнаётся опросом, вход работает
			logger.Warn("sms.ru: адрес оповещений не заведён", slog.Any("error", err))
		}
	}

	return client
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

	redisClient, err := redis.Open(ctx, cfg.Redis.URL)
	if err != nil {
		return err
	}
	defer func() { _ = redisClient.Close() }()

	registry := telemetry.NewRegistry()
	systemClock := clock.System{}
	identities := random.IDs{}
	uow := postgres.NewUnitOfWork(store.pool)
	platform := platformapi.New(platformapi.Config{BaseURL: cfg.Platform.URL, Token: cfg.Platform.Token, Timeout: cfg.Platform.Timeout})
	directory := directoryOf(store, cfg)

	projector := appcatalog.NewProjector(uow, systemClock)
	indexer := appcatalog.NewIndexer(uow, store.index, systemClock, indexBatch)
	queries := appcatalog.NewQueries(uow, store.index)
	participants := appparticipant.NewService(uow, systemClock)
	tenantRegistry := appparticipant.NewRegistry(uow, systemClock)
	shopSettings := appparticipant.NewSettings(uow, platform, systemClock)
	auth := appbuyer.NewAuth(uow, callCheck(ctx, cfg, logger), redis.NewSessions(redisClient, keyPrefix), redis.NewLimiter(redisClient, keyPrefix),
		identities, systemClock, appbuyer.AuthConfig{
			ChallengeTTL: cfg.Login.ChallengeTTL, CheckInterval: cfg.Login.CheckInterval, SessionTTL: cfg.Login.SessionTTL,
			PhoneLimit: appbuyer.Limit(cfg.Login.PhoneLimit), IPLimit: appbuyer.Limit(cfg.Login.IPLimit),
		})
	carts := appcart.NewService(uow, store.index, platform, identities, systemClock, cfg.Cart.TTL)
	checkouts := appcheckout.NewService(uow, store.index, platform, directory, identities, systemClock,
		appcheckout.Config{QuoteTTL: cfg.Checkout.QuoteTTL, QuoteTimeout: cfg.Checkout.QuoteTimeout})

	dispatcher := messaging.NewDispatcher(postgres.NewTaskQueue(store.pool), systemClock, messaging.DispatcherConfig{
		Batch: cfg.Outbox.Batch, PerTenant: cfg.Outbox.PerTenant, Lease: cfg.Outbox.Lease,
	}, map[string]messaging.Handler{
		appcatalog.TaskSyncOffer:  indexer.HandleTask,
		appcatalog.TaskSyncTenant: indexer.HandleTask,
	})
	janitor := messaging.NewJanitor(postgres.NewRetention(store.pool), systemClock, messaging.RetentionPolicy{
		ProcessedEvents: cfg.Retention.ProcessedEvents, OutboxDone: cfg.Retention.OutboxDone,
		ParkedEvents: cfg.Retention.ParkedEvents, Challenges: cfg.Retention.Challenges, Batch: 1000,
	})

	built, err := indexer.EnsureIndex(ctx)
	if err != nil {
		return fmt.Errorf("индекс предложений: %w", err)
	}
	if built {
		logger.Info("индекс предложений собран из проекции")
	}

	if err := directory.EnsureIndex(ctx); err != nil {
		return fmt.Errorf("индекс пунктов выдачи: %w", err)
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
			Auth:          auth,
			Carts:         carts,
			Directory:     directory,
			Checkouts:     checkouts,
			OperatorToken: cfg.Operator.Token,
			TrustProxy:    cfg.Login.TrustProxy,
			Checks: []transport.Check{
				{Name: "postgres", Probe: store.pool.Ping},
				{Name: "opensearch", Probe: store.index.Ping},
				{Name: "redis", Probe: func(ctx context.Context) error { return redisClient.Ping(ctx).Err() }},
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
	group.Go(func() error {
		return jobs.NewPickupRefreshRunner(directory, cfg.Delivery.RefreshInterval, logger, registry).Run(groupCtx)
	})
	group.Go(func() error {
		return jobs.NewSettingsSyncRunner(shopSettings, cfg.Settings.SyncInterval, logger, registry).Run(groupCtx)
	})

	logger.Info("маркетплейс запущен", slog.String("group", cfg.Kafka.GroupID), slog.Any("topics", topics))

	return group.Wait()
}
