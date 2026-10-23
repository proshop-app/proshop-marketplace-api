package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/cdek"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/opensearch"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/smsru"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/yandex"
)

const (
	// indexBatch — предложений за один проход синхронизации индекса.
	indexBatch = 500
	// retentionBatch — записей за один проход чистки.
	retentionBatch = 1000
	// keyPrefix — префикс ключей маркетплейса в Redis.
	keyPrefix = "marketplace:"
)

// storage — база и индексы процесса.
type storage struct {
	pool    *pgxpool.Pool
	index   *opensearch.Index
	pickups *opensearch.PickupIndex
}

// openStorage подключает PostgreSQL и OpenSearch.
func openStorage(ctx context.Context, cfg config.Config) (*storage, error) {
	pool, err := postgres.Open(ctx, cfg.Database.URL, cfg.Database.MaxConns)
	if err != nil {
		return nil, err
	}

	index, err := opensearch.New(opensearch.Config{
		URLs:               cfg.OpenSearch.URLs,
		Username:           cfg.OpenSearch.Username,
		Password:           cfg.OpenSearch.Password,
		InsecureSkipVerify: cfg.OpenSearch.InsecureSkipVerify,
		Alias:              cfg.OpenSearch.Index,
		Shards:             cfg.OpenSearch.Shards,
		Replicas:           cfg.OpenSearch.Replicas,
	})
	if err != nil {
		pool.Close()

		return nil, err
	}

	return &storage{
		pool:    pool,
		index:   index,
		pickups: opensearch.NewPickupIndex(index, cfg.Delivery.PickupIndex),
	}, nil
}

// close закрывает соединения с базой.
func (s *storage) close() {
	s.pool.Close()
}

// directory — справочники пунктов выдачи подключённых перевозчиков.
func directory(store *storage, cfg config.Config) *appdelivery.Directory {
	return appdelivery.NewDirectory(
		postgres.NewUnitOfWork(store.pool),
		carriers(cfg),
		store.pickups,
		postgres.NewLeases(store.pool),
		clock.System{},
		holder(),
	)
}

// carriers — перевозчики, для которых в окружении есть учётная запись платформы.
func carriers(cfg config.Config) []ports.PickupPointsProvider {
	var providers []ports.PickupPointsProvider

	if c := cfg.Delivery.CDEK; c.ClientID != "" {
		providers = append(providers, cdek.New(cdek.Config{
			BaseURL:      c.URL,
			RegionCodes:  c.RegionCodes,
			ClientID:     c.ClientID,
			ClientSecret: c.ClientSecret,
		}))
	}

	if y := cfg.Delivery.Yandex; y.Token != "" {
		providers = append(providers, yandex.New(yandex.Config{
			BaseURL:     y.URL,
			RegionCodes: y.RegionCodes,
			Token:       y.Token,
		}))
	}

	return providers
}

// callCheck — шлюз проверки звонком: живой sms.ru или песочница для разработки.
func callCheck(ctx context.Context, cfg config.Config, logger *slog.Logger) ports.CallCheck {
	if cfg.SmsRu.Mode == config.SmsRuSandbox {
		logger.Warn(
			"вход по звонку в песочнице: любой номер подтверждается сам, только для разработки",
			slog.Duration("confirmAfter", cfg.SmsRu.SandboxConfirmAfter),
		)

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

// holder — имя процесса для аренды фоновых задач.
func holder() string {
	host, err := os.Hostname()
	if err != nil {
		host = "marketplace"
	}

	return fmt.Sprintf("%s:%d", host, os.Getpid())
}
