package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appoperator "github.com/proshop-app/proshop-marketplace/internal/application/operator"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	infrakafka "github.com/proshop-app/proshop-marketplace/internal/infrastructure/kafka"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/password"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/platformapi"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/telemetry"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

// services — сценарии приложения процесса.
type services struct {
	operatorAuth *appoperator.Auth
	events       *messaging.Events
	taxonomy     *apptaxonomy.Service
	projector    *appcatalog.Projector
	indexer      *appcatalog.Indexer
	queries      *appcatalog.Queries
	participants *appparticipant.Service
	registry     *appparticipant.Registry
	settings     *appparticipant.Settings
	auth         *appbuyer.Auth
	carts        *appcart.Service
	directory    *appdelivery.Directory
	checkouts    *appcheckout.Service
	dispatcher   *messaging.Dispatcher
	janitor      *messaging.Janitor
	parker       *messaging.Parker
}

// server — процесс маркетплейса: хранилища, сценарии и метрики.
type server struct {
	cfg     config.Config
	logger  *slog.Logger
	store   *storage
	cache   *goredis.Client
	metrics *prometheus.Registry
	app     services
}

// serve — HTTP, консьюмер Kafka и фоновые задачи до сигнала остановки.
func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if cfg.Operator.PasswordHash != "" {
		if err := password.Validate(cfg.Operator.PasswordHash); err != nil {
			return err
		}
	}
	store, err := openStorage(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.close()

	cache, err := redis.Open(ctx, cfg.Redis.URL)
	if err != nil {
		return err
	}
	defer func() { _ = cache.Close() }()
	s := &server{cfg: cfg, logger: logger, store: store, cache: cache, metrics: telemetry.NewRegistry()}
	s.app, err = s.services(ctx)
	if err != nil {
		return err
	}

	if err := s.prepareIndexes(ctx); err != nil {
		return err
	}

	return s.run(ctx)
}

// services собирает сценарии приложения поверх хранилищ и внешних адаптеров.
func (s *server) services(ctx context.Context) (services, error) {
	tokens, err := s.identityTokens(
		s.cfg.Identity.ClientID,
		s.cfg.Identity.Secret,
		"marketplace",
		s.cfg.Login.SessionTTL,
	)
	if err != nil {
		return services{}, err
	}
	cfg := s.cfg
	now := clock.System{}
	identities := random.IDs{}
	uow := postgres.NewUnitOfWork(s.store.pool)
	platform := platformapi.New(platformapi.Config{
		BaseURL: cfg.Platform.URL,
		Token:   cfg.Platform.Token,
		Timeout: cfg.Platform.Timeout,
	})
	// корзина и покупка узнают об отказе участия первыми — по нему сверка настроек внеочередная
	resyncing := appparticipant.NewResyncingPlatform(platform, uow, now)
	settings := appparticipant.NewSettings(uow, platform, now)
	indexer := appcatalog.NewIndexer(uow, s.store.index, now, indexBatch)
	pickups := directory(s.store, cfg)
	auth := appbuyer.NewAuth(
		uow,
		callCheck(ctx, cfg, s.logger),
		tokens,
		redis.NewLimiter(s.cache, keyPrefix),
		identities,
		now,
		appbuyer.AuthConfig{
			ChallengeTTL:  cfg.Login.ChallengeTTL,
			CheckInterval: cfg.Login.CheckInterval,
			SessionTTL:    cfg.Login.SessionTTL,
			PhoneLimit:    appbuyer.Limit(cfg.Login.PhoneLimit),
			IPLimit:       appbuyer.Limit(cfg.Login.IPLimit),
		},
	)
	checkouts := appcheckout.NewService(uow, s.store.index, resyncing, pickups, identities, now, appcheckout.Config{
		DeliverySubsidyMinPercent: cfg.Checkout.DeliverySubsidyMinPercent,
		QuoteTTL:                  cfg.Checkout.QuoteTTL,
		ReturnBaseURL:             cfg.Checkout.ReturnBaseURL,
		CartTTL:                   cfg.Cart.TTL,
		QuoteTimeout:              cfg.Checkout.QuoteTimeout,
	})
	projector := appcatalog.NewProjector(uow, now)
	registry := appparticipant.NewRegistry(uow, now)
	replay := kafka.NewReplay(kafka.NewRouter(projector, registry), cfg.Kafka.TopicPrefix)
	events := messaging.NewEvents(uow, now, replay)
	var operatorTokens ports.IdentityTokens
	if cfg.Operator.Email != "" {
		operatorTokens, err = s.identityTokens(
			cfg.Identity.OperatorClientID,
			cfg.Identity.OperatorSecret,
			"platform",
			cfg.Operator.SessionTTL,
		)
		if err != nil {
			return services{}, err
		}
	}
	operatorAuth := appoperator.NewAuth(
		operatorTokens,
		redis.NewLimiter(s.cache, keyPrefix),
		password.Verifier{},
		identities,
		now,
		appoperator.Config{
			Principal: operator.Principal{
				ID:    cfg.Operator.Email,
				Email: cfg.Operator.Email,
				Name:  cfg.Operator.Name,
				Role:  operator.Role,
			},
			PasswordHash: cfg.Operator.PasswordHash,
			SessionTTL:   cfg.Operator.SessionTTL,
		},
	)
	dispatcher := messaging.NewDispatcher(
		postgres.NewTaskQueue(s.store.pool),
		now,
		messaging.DispatcherConfig{
			Batch:     cfg.Outbox.Batch,
			PerTenant: cfg.Outbox.PerTenant,
			Lease:     cfg.Outbox.Lease,
		},
		map[string]messaging.Handler{
			messaging.TaskReplay:            events.HandleTask,
			appcatalog.TaskSyncOffer:        indexer.HandleTask,
			appcatalog.TaskSyncTenant:       indexer.HandleTask,
			appparticipant.TaskSyncSettings: settings.HandleTask,
		},
	)
	janitor := messaging.NewJanitor(postgres.NewRetention(s.store.pool), now, messaging.RetentionPolicy{
		ProcessedEvents: cfg.Retention.ProcessedEvents,
		OutboxDone:      cfg.Retention.OutboxDone,
		ParkedEvents:    cfg.Retention.ParkedEvents,
		Challenges:      cfg.Retention.Challenges,
		Batch:           retentionBatch,
	})

	return services{
		operatorAuth: operatorAuth,
		events:       events,
		taxonomy:     apptaxonomy.NewService(uow, now),
		projector:    projector,
		indexer:      indexer,
		queries:      appcatalog.NewQueries(uow, s.store.index, resyncing),
		participants: appparticipant.NewService(uow, now),
		registry:     registry,
		settings:     settings,
		auth:         auth,
		carts:        appcart.NewService(uow, s.store.index, resyncing, identities, now, cfg.Cart.TTL),
		directory:    pickups,
		checkouts:    checkouts,
		dispatcher:   dispatcher,
		janitor:      janitor,
		parker:       messaging.NewParker(uow),
	}, nil
}

// prepareIndexes заводит индексы, которых нет: первый запуск или потеря кластера.
func (s *server) prepareIndexes(ctx context.Context) error {
	built, err := s.app.indexer.EnsureIndex(ctx)
	if err != nil {
		return fmt.Errorf("индекс предложений: %w", err)
	}
	if built {
		s.logger.Info("индекс предложений собран из проекции")
	}

	if err := s.app.directory.EnsureIndex(ctx); err != nil {
		return fmt.Errorf("индекс пунктов выдачи: %w", err)
	}

	return nil
}

// run запускает HTTP, консьюмер и фоновые циклы; первый сбой останавливает остальных.
func (s *server) run(ctx context.Context) error {
	cfg := s.cfg
	router := kafka.NewRouter(s.app.projector, s.app.registry)
	topics := kafka.FullTopics(cfg.Kafka.TopicPrefix, router)
	reader := infrakafka.NewReader(infrakafka.Config{
		Brokers: cfg.Kafka.Brokers,
		GroupID: cfg.Kafka.GroupID,
		Topics:  topics,
		Logger:  s.logger,
	})
	consumerConfig := kafka.ConsumerConfig{
		TopicPrefix: cfg.Kafka.TopicPrefix,
		MaxAttempts: cfg.Kafka.MaxAttempts,
		Backoff:     messaging.Backoff,
		Ready:       s.store.pool.Ping,
		Now:         clock.System{}.Now,
	}
	consumer := kafka.NewConsumer(reader, router, s.app.parker, consumerConfig, s.logger, s.metrics)
	httpServer := s.httpServer()
	group, groupCtx := errgroup.WithContext(ctx)

	group.Go(func() error {
		s.logger.Info("HTTP слушает", slog.String("addr", cfg.HTTP.Addr))

		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP: %w", err)
		}

		return nil
	})
	group.Go(func() error {
		<-groupCtx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(groupCtx), cfg.HTTP.ShutdownTimeout)
		defer cancel()

		return httpServer.Shutdown(shutdownCtx)
	})
	group.Go(func() error {
		err := consumer.Run(groupCtx)

		if closeErr := reader.Close(); closeErr != nil {
			s.logger.Warn("kafka: читатель закрылся с ошибкой", slog.Any("error", closeErr))
		}

		return err
	})

	for _, job := range s.jobs() {
		group.Go(func() error { return job(groupCtx) })
	}

	s.logger.Info("маркетплейс запущен", slog.String("group", cfg.Kafka.GroupID), slog.Any("topics", topics))

	return group.Wait()
}

// jobs — фоновые циклы процесса.
func (s *server) jobs() []func(context.Context) error {
	cfg := s.cfg

	return []func(context.Context) error{
		jobs.NewOrdersRunner(s.app.checkouts, cfg.Checkout.OrdersInterval, s.logger).Run,
		jobs.NewOutboxRunner(s.app.dispatcher, cfg.Outbox.PollInterval, cfg.Outbox.Batch, s.logger, s.metrics).Run,
		jobs.NewRetentionRunner(s.app.janitor, cfg.Retention.Interval, s.logger).Run,
		jobs.NewPickupRefreshRunner(s.app.directory, cfg.Delivery.RefreshInterval, s.logger, s.metrics).Run,
		jobs.NewSettingsSyncRunner(s.app.settings, cfg.Settings.SyncInterval, s.logger, s.metrics).Run,
	}
}

// httpServer — HTTP с маршрутами маркетплейса и проверками зависимостей.
func (s *server) httpServer() *http.Server {
	cfg := s.cfg
	checks := []transport.Check{
		{Name: "postgres", Probe: s.store.pool.Ping},
		{Name: "opensearch", Probe: s.store.index.Ping},
		{Name: "redis", Probe: func(ctx context.Context) error { return s.cache.Ping(ctx).Err() }},
		{Name: "kafka", Probe: func(ctx context.Context) error { return infrakafka.Ping(ctx, cfg.Kafka.Brokers) }},
	}
	handler := transport.NewHandler(transport.Deps{
		Taxonomy:       s.app.taxonomy,
		Catalog:        s.app.queries,
		Participants:   s.app.participants,
		Auth:           s.app.auth,
		Carts:          s.app.carts,
		Directory:      s.app.directory,
		Checkouts:      s.app.checkouts,
		OperatorToken:  cfg.Operator.Token,
		OperatorAuth:   s.app.operatorAuth,
		Events:         s.app.events,
		OperatorOrigin: cfg.Operator.Origin,
		TrustProxy:     cfg.Login.TrustProxy,
		Checks:         checks,
		Metrics:        promhttp.HandlerFor(s.metrics, promhttp.HandlerOpts{}),
		Registerer:     s.metrics,
		Logger:         s.logger,
	})

	return &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}
