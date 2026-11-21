package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
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
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	appoperator "github.com/proshop-app/proshop-marketplace/internal/application/operator"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/platformguard"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apppromotion "github.com/proshop-app/proshop-marketplace/internal/application/promotion"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	appsynonyms "github.com/proshop-app/proshop-marketplace/internal/application/synonyms"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	infrakafka "github.com/proshop-app/proshop-marketplace/internal/infrastructure/kafka"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/localgeoip"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/password"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/platformapi"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/referencecatalog"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/searchapi"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/storecache"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/synonymmodel"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/telemetry"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

// referenceStatsInterval — как часто обновлять метрики ревизии и очереди кандидатов реестра.
const (
	referenceStatsInterval    = time.Minute
	taxonomyDisplayInterval   = 5 * time.Second
	taxonomySelectionInterval = 15 * time.Second
)

// services — сценарии приложения процесса.
type services struct {
	cities        *appbuyer.Cities
	statuses      *moderationstatus.Service
	buyerFeatures *appbuyer.Features

	promotion         *apppromotion.Service
	moderationMetrics *jobs.ModerationMetrics
	moderation        *appmoderation.Service
	operatorAuth      *appoperator.Auth
	events            *messaging.Events
	taxonomy          *apptaxonomy.Service
	projector         *appcatalog.Projector
	indexer           *appcatalog.Indexer
	queries           *appcatalog.Queries
	participants      *appparticipant.Service
	registry          *appparticipant.Registry
	settings          *appparticipant.Settings
	auth              *appbuyer.Auth
	carts             *appcart.Service
	directory         *appdelivery.Service
	checkouts         *appcheckout.Service
	dispatcher        *messaging.Dispatcher
	creation          *appcheckout.Executor
	janitor           *messaging.Janitor
	parker            *messaging.Parker
	reference         *appreference.Service
	synonyms          *appsynonyms.Service
	candidates        ports.ReferenceCandidates
	// writer публикует события площадки; закрывается после остановки разбора outbox
	writer *infrakafka.Writer
}

// server — процесс маркетплейса: хранилища, сценарии и метрики.
type server struct {
	cityDetector *localgeoip.Detector
	cfg          config.Config
	logger       *slog.Logger
	store        *storage
	cache        *goredis.Client
	metrics      *prometheus.Registry
	app          services
}

// promotionRandom использует math/rand/v2: ротации не нужна криптографическая случайность.
type promotionRandom struct{}

// serve — HTTP, консьюмер Kafka и фоновые задачи до сигнала остановки.
func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
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

	cache, err := redis.NewClient(cfg.Redis.URL)
	if err != nil {
		return err
	}
	defer func() { _ = cache.Close() }()
	s := &server{cfg: cfg, logger: logger, store: store, cache: cache, metrics: telemetry.NewRegistry()}
	s.cityDetector, err = localgeoip.New(cfg.City.Path, cfg.City.DevClientIP, logger, s.metrics)
	if err != nil {
		return err
	}
	defer func() { _ = s.cityDetector.Close() }()
	s.app, err = s.services(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.app.writer.Close() }()

	if err := s.prepareIndexes(ctx); err != nil {
		return err
	}

	if err := s.app.queries.RefreshTaxonomyDisplay(ctx); err != nil {
		return fmt.Errorf("снимок отображения таксономии: %w", err)
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
	uow := storecache.NewUnitOfWork(
		postgres.NewUnitOfWork(s.store.pool),
		redis.NewStoreCardCache(s.cache, keyPrefix),
	)
	upstream := platformapi.New(platformapi.Config{
		BaseURL: cfg.Platform.URL,
		Token:   cfg.Platform.Token,
		Timeout: cfg.Platform.Timeout,
	})
	platform := platformguard.New(
		upstream,
		platformguard.Config{
			ReadTimeout:           cfg.Platform.ReadTimeout,
			StrictTimeout:         cfg.Platform.Timeout,
			CacheTTL:              cfg.Platform.CacheTTL,
			StaleTTL:              cfg.Platform.StaleTTL,
			CacheSize:             cfg.Platform.CacheSize,
			MaxConcurrent:         cfg.Platform.MaxConcurrent,
			TenantMaxConcurrent:   cfg.Platform.TenantMaxConcurrent,
			CheckoutMaxConcurrent: cfg.Platform.CheckoutMaxConcurrent,
			ReadMaxConcurrent:     cfg.Platform.ReadMaxConcurrent,
			QueueSize:             cfg.Platform.QueueSize,
			TenantQueueSize:       cfg.Platform.TenantQueueSize,
			TenantStateSize:       cfg.Platform.TenantStateSize,
			TenantIdleTTL:         cfg.Platform.TenantIdleTTL,
			FailureThreshold:      cfg.Platform.FailureThreshold,
			FailureWindow:         cfg.Platform.FailureWindow,
			FailureMinCalls:       cfg.Platform.FailureMinCalls,
			FailurePercent:        cfg.Platform.FailurePercent,
			OpenInterval:          cfg.Platform.OpenInterval,
			LogInterval:           cfg.Platform.LogInterval,
		},
		now,
		telemetry.NewPlatform(s.metrics),
		s.logger,
	)
	// корзина и покупка узнают об отказе участия первыми — по нему сверка настроек внеочередная
	resyncing := appparticipant.NewResyncingPlatform(platform, uow, now)
	settings := appparticipant.NewSettings(uow, platform, now, identities)
	var candidates ports.ReferenceCandidates
	if cfg.Reference.CandidatesInterval > 0 && cfg.Search.CandidatesToken != "" {
		candidates, err = searchapi.NewCandidates(searchapi.Config{
			URL:     cfg.Search.URL,
			Token:   cfg.Search.CandidatesToken,
			Timeout: cfg.Search.Timeout,
		})
		if err != nil {
			return services{}, err
		}
	}
	writer := infrakafka.NewWriter(cfg.Kafka.Brokers)
	participants := appparticipant.NewService(uow, now, identities).WithProbationOffers(cfg.Moderation.ProbationOffers)
	statuses := moderationstatus.NewService(
		uow,
		now,
		writer,
		cfg.Kafka.FullTopic(cfg.Kafka.OfferStatusTopic),
		cfg.Kafka.FullTopic(cfg.Kafka.ParticipantStatusTopic),
	)
	references := appreference.NewService(uow, now, identities, writer, appreference.Config{
		Topic: cfg.Kafka.FullTopic(cfg.Kafka.ReferenceTopic),
	})
	synonyms := s.searchSynonyms(uow, now, writer)
	models := appcatalog.NewModelKeys(
		referencecatalog.New(references),
		telemetry.NewModelKeys(s.metrics),
		cfg.ModelKeys,
	)
	indexer := appcatalog.NewIndexer(
		uow,
		s.store.index,
		now,
		indexBatch,
		postgres.NewIndexRebuildLock(s.store.pool),
		models,
		cfg.Settings.StoreCardRefreshDelay,
	)
	pickups := appdelivery.NewService(uow, resyncing, redis.NewPickupCache(s.cache, keyPrefix), appdelivery.Config{
		Attempts:     cfg.Delivery.Attempts,
		TTL:          cfg.Delivery.CacheTTL,
		QuoteTimeout: cfg.Checkout.QuoteTimeout,
	})
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
	carts := appcart.NewService(uow, s.store.index, resyncing, identities, now, cfg.Cart.TTL)
	checkouts := appcheckout.NewService(
		uow,
		resyncing,
		pickups,
		carts,
		identities,
		now,
		appcheckout.Config{
			OrderCallTimeout: cfg.Platform.Timeout,
			QuoteTTL:         cfg.Checkout.QuoteTTL,
			ReturnBaseURL:    cfg.Checkout.ReturnBaseURL,
			PaymentStub:      cfg.Checkout.PaymentStub,
			CartTTL:          cfg.Cart.TTL,
			QuoteTimeout:     cfg.Checkout.QuoteTimeout,
		},
	)
	moderationMetrics := jobs.NewModerationMetrics(s.metrics)
	moderation := appmoderation.NewService(uow, now, identities, appmoderation.Config{
		Metrics:         moderationMetrics,
		ProbationOffers: cfg.Moderation.ProbationOffers,
		RequireImage:    cfg.Moderation.RequireImage,
	})
	projector := appcatalog.NewProjector(uow, now, moderation)
	registry := appparticipant.NewRegistry(uow, now, identities)
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

	checkouts.SetObserver(jobs.NewCheckoutMetrics(s.logger, s.metrics))
	dispatcher := messaging.NewDispatcher(
		postgres.NewTaskQueue(s.store.pool),
		now,
		messaging.DispatcherConfig{
			MaxAttempts: cfg.Outbox.MaxAttempts,
			// Эти обработчики публикуют или применяют снимок без внешней версии.
			OrderedKinds: []string{
				appparticipant.TaskSyncSettings,
				moderationstatus.TaskParticipant,
				appreference.TaskChanged,
				appsynonyms.TaskChanged,
			},
			ExcludeKinds: []string{
				appcheckout.TaskCreateOrder,
				appcheckout.TaskExpireCreation,
				appcheckout.TaskPollOrder,
				appcheckout.TaskCancelOrder,
			},
			Batch:     cfg.Outbox.Batch,
			PerTenant: cfg.Outbox.PerTenant,
			Lease:     cfg.Outbox.Lease,
		},
		map[string]messaging.Handler{
			appmoderation.TaskReevaluate:     moderation.HandleTask,
			appmoderation.TaskAllowTrusted:   moderation.HandleTask,
			messaging.TaskReplay:             events.HandleTask,
			appcatalog.TaskSyncOffer:         indexer.HandleTask,
			appcatalog.TaskSyncTenant:        indexer.HandleTask,
			appparticipant.TaskSyncSettings:  settings.HandleTask,
			ports.TaskStoreCard:              participants.RefreshStoreCard,
			appreference.TaskChanged:         references.HandleTask,
			appsynonyms.TaskChanged:          synonyms.HandleTask,
			moderationstatus.TaskOffer:       statuses.HandleTask,
			moderationstatus.TaskParticipant: statuses.HandleTask,
		},
	)
	indexer.SetBatchObserver(telemetry.NewOfferBatches(s.metrics))
	dispatcher.RegisterBatch(appcatalog.TaskSyncOffer, indexer.HandleOfferBatch)
	dispatcher.RegisterBatch(moderationstatus.TaskOffer, statuses.HandleOfferBatch)
	janitor := messaging.NewJanitor(postgres.NewRetention(s.store.pool), now, messaging.RetentionPolicy{
		ProcessedEvents: cfg.Retention.ProcessedEvents,
		OutboxDone:      cfg.Retention.OutboxDone,
		ParkedEvents:    cfg.Retention.ParkedEvents,
		Challenges:      cfg.Retention.Challenges,
		Batch:           retentionBatch,
	})

	features := postgres.NewCatalogFeatures(s.store.pool)
	queries := appcatalog.NewQueries(uow, s.store.index, resyncing, features, features, now)
	location, err := time.LoadLocation(cfg.Promotion.Timezone)
	if err != nil {
		return services{}, err
	}
	promotions := apppromotion.NewService(uow, now, identities, s.store.index, apppromotion.Config{
		Catalog:         queries,
		Limiter:         redis.NewLimiter(s.cache, keyPrefix),
		EventsPerMinute: cfg.Promotion.EventsPerMinute,
		Location:        location,
		SearchDepth:     cfg.Promotion.SearchRelevanceDepth,
		VisitorSalt:     cfg.Promotion.VisitorSalt,
		Random:          promotionRandom{},
		Metrics:         telemetry.NewPromotion(s.metrics),
	})
	return services{
		cities: appbuyer.NewCities(
			s.cityDetector,
			upstream,
			redis.NewLimiter(s.cache, keyPrefix),
		),
		buyerFeatures: appbuyer.NewFeatures(
			uow,
			s.cityDetector,
			pickups,
		),

		promotion:         promotions,
		moderationMetrics: moderationMetrics,
		moderation:        moderation,
		operatorAuth:      operatorAuth,
		events:            events,
		taxonomy:          apptaxonomy.NewService(uow, now, moderation),
		projector:         projector,
		indexer:           indexer,
		queries:           queries,
		participants:      participants,
		statuses:          statuses,
		registry:          registry,
		settings:          settings,
		auth:              auth,
		carts:             carts,
		directory:         pickups,
		checkouts:         checkouts,
		dispatcher:        dispatcher,
		creation: appcheckout.NewExecutor(
			postgres.NewTaskQueue(s.store.pool),
			now,
			checkouts,
			cfg.Outbox.MaxAttempts,
		),
		janitor:    janitor,
		parker:     messaging.NewParker(uow),
		reference:  references,
		synonyms:   synonyms,
		candidates: candidates,
		writer:     writer,
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

	return nil
}

// run запускает HTTP, консьюмеры и фоновые циклы; первый сбой останавливает остальных.
func (s *server) run(ctx context.Context) error {
	cfg := s.cfg
	httpServer := s.httpServer()
	group, groupCtx := errgroup.WithContext(ctx)

	group.Go(func() error {
		s.logger.Info(
			"HTTP слушает",
			slog.String("addr", cfg.HTTP.Addr),
			slog.Int("trustedProxies", cfg.HTTP.TrustedProxies),
		)

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
	s.runConsumers(groupCtx, group)

	for _, job := range s.jobs() {
		group.Go(func() error { return job(groupCtx) })
	}

	s.logger.Info("маркетплейс запущен")

	return group.Wait()
}

// jobs — фоновые циклы процесса.
func (s *server) jobs() []func(context.Context) error {
	cfg := s.cfg

	runners := []func(context.Context) error{
		s.cityDetector.Run,
		s.refreshTaxonomyDisplay,
		s.refreshTaxonomySelection,
		func(ctx context.Context) error {
			return s.app.moderationMetrics.Run(ctx, s.app.moderation, cfg.Moderation.MetricsInterval, s.logger)
		},
	}
	if s.app.candidates != nil {
		runners = append(runners, jobs.NewReferenceCandidatesRunner(
			s.app.reference,
			s.app.candidates,
			cfg.Reference.CandidatesInterval,
			s.logger,
			s.metrics,
		).WithSchedule(postgres.NewScheduledWork(
			s.store.pool,
			"reference-candidates",
			cfg.Reference.CandidatesInterval,
		)).Run)
	}
	if interval := cfg.Synonyms.Interval; interval > 0 {
		schedule := postgres.NewScheduledWork(s.store.pool, "reference-search-synonyms", interval)
		runner := jobs.NewReferenceSynonymsRunner(s.app.synonyms, schedule, interval, s.logger, s.metrics)
		runners = append(runners, func(ctx context.Context) error {
			return runner.Run(ports.WithDatabaseBudget(ctx, ports.DatabaseBudget(cfg.Database.Background)))
		})
	}
	outbox := jobs.NewOutboxRunner(s.app.dispatcher, cfg.Outbox.PollInterval, s.logger, s.metrics)
	return append(runners, []func(context.Context) error{
		func(ctx context.Context) error {
			return jobs.RunTaxonomyRevalidation(ctx, s.app.taxonomy, s.logger)
		},
		jobs.NewPromotionRunner(
			s.app.promotion,
			postgres.NewPromotionLock(s.store.pool),
			s.logger,
			s.cfg.Promotion.TickInterval,
		).Run,
		jobs.NewIndexMaintenanceRunner(s.app.indexer, cfg.Search.MaintenanceInterval, s.logger, s.metrics).Run,
		jobs.NewCreationRunner(s.app.creation, s.logger, outbox.RecordOutcome).Run,
		jobs.NewOrdersRunner(s.app.checkouts, cfg.Checkout.OrdersInterval, s.logger).Run,
		jobs.NewOutboxRetryStatsRunner(
			s.app.dispatcher,
			clock.System{},
			cfg.Outbox.RetryMetricsInterval,
			s.logger,
			s.metrics,
		).Run,
		outbox.Run,
		jobs.NewRetentionRunner(s.app.janitor, cfg.Retention.Interval, s.logger).Run,
		jobs.NewSettingsSyncRunner(s.app.settings, cfg.Settings.SyncInterval, s.logger, s.metrics).
			WithSchedule(postgres.NewScheduledWork(s.store.pool, "settings-sync", cfg.Settings.SyncInterval)).Run,
		jobs.NewReferenceStatsRunner(s.app.reference, referenceStatsInterval, s.logger, s.metrics).
			WithSchedule(postgres.NewScheduledWork(s.store.pool, "reference-stats", referenceStatsInterval)).Run,
		jobs.NewModelKeyStatsRunner(postgres.NewModelKeyStats(s.store.pool), s.logger, s.metrics).
			WithSchedule(postgres.NewScheduledWork(s.store.pool, "model-key-stats", jobs.ModelKeyStatsInterval)).Run,
	}...)
}

// httpServer — HTTP с маршрутами маркетплейса и проверками зависимостей.
func (s *server) httpServer() *http.Server {
	cfg := s.cfg
	checks := []transport.Check{
		{Name: "postgres", Probe: s.store.pool.Ping},
		{
			Name:  "search",
			Probe: s.store.index.Ping,
		},
		{Name: "redis", Optional: true, Probe: func(ctx context.Context) error { return s.cache.Ping(ctx).Err() }},
		{
			Name:     "kafka",
			Optional: true,
			Probe:    func(ctx context.Context) error { return infrakafka.Ping(ctx, cfg.Kafka.Brokers) },
		},
	}
	handler := transport.NewHandler(transport.Deps{
		DatabaseBudgets: ports.DatabaseBudgets{
			Read:       ports.DatabaseBudget(cfg.Database.Read),
			Checkout:   ports.DatabaseBudget(cfg.Database.Checkout),
			Ingestion:  ports.DatabaseBudget(cfg.Database.Ingestion),
			Background: ports.DatabaseBudget(cfg.Database.Background),
		},
		RequestTimeout:         cfg.HTTP.RequestTimeout,
		ReferenceImportTimeout: cfg.HTTP.ReferenceImportTimeout,
		Statuses:               s.app.statuses,
		Features:               s.app.buyerFeatures,
		Cities:                 s.app.cities,

		Promotion:      s.app.promotion,
		ServerAPIToken: cfg.Promotion.ServerAPIToken,
		Moderation:     s.app.moderation,
		Taxonomy:       s.app.taxonomy,
		Catalog:        s.app.queries,
		SellersCounts:  postgres.NewSellersCounts(s.store.pool),
		Participants:   s.app.participants,
		Auth:           s.app.auth,
		Carts:          s.app.carts,
		Directory:      s.app.directory,
		Checkouts:      s.app.checkouts,
		Reference:      s.app.reference,
		Synonyms:       s.app.synonyms,
		OperatorToken:  cfg.Operator.Token,
		// потребители реестра читают своим токеном, мутации — только у оператора
		ReferenceReadToken: cfg.Reference.ReadToken,
		OperatorAuth:       s.app.operatorAuth,
		Events:             s.app.events,
		Dispatcher:         s.app.dispatcher,
		OperatorOrigin:     cfg.Operator.Origin,
		TrustedProxies:     cfg.HTTP.TrustedProxies,
		Checks:             checks,
		Metrics:            promhttp.HandlerFor(s.metrics, promhttp.HandlerOpts{}),
		Registerer:         s.metrics,
		Logger:             s.logger,
	})

	return &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      cfg.HTTP.RequestTimeout + 5*time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

func (promotionRandom) IntN(n int) int {
	// #nosec G404 -- Ротация не использует криптографических секретов.
	return mathrand.IntN(n)
}

func (s *server) searchSynonyms(uow ports.UnitOfWork, now ports.Clock, writer ports.Publisher) *appsynonyms.Service {
	cfg := s.cfg
	topic := cfg.Kafka.FullTopic(appsynonyms.EventTopic)
	var validator ports.SynonymValidator
	if cfg.Reference.ReadToken != "" {
		validator = s.store.index.SynonymValidator(cfg.Reference.ReadToken)
	}
	service := appsynonyms.New(uow, now, validator, writer, topic)
	if cfg.Synonyms.ModelURL == "" {
		return service
	}
	model, err := synonymmodel.New(
		cfg.Synonyms.ModelURL,
		cfg.Synonyms.ModelToken,
		cfg.Synonyms.ModelName,
		cfg.Search.Timeout,
	)
	if err != nil {
		s.logger.Error("поисковые синонимы: модель отключена", slog.Any("error", err))
		return service
	}
	return service.WithModel(model)
}
