package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/dadata"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/searchapi"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/smsru"
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
	pool  *pgxpool.Pool
	index *searchapi.Index
}

// openStorage подключает PostgreSQL и API поиска.
func openStorage(ctx context.Context, cfg config.Config) (*storage, error) {
	pool, err := postgres.Open(ctx, cfg.Database.URL, cfg.Database.MaxConns)
	if err != nil {
		return nil, err
	}

	index, err := searchapi.New(searchapi.Config{
		URL:     cfg.Search.URL,
		Token:   cfg.Search.Token,
		Timeout: cfg.Search.Timeout,
	})
	if err != nil {
		pool.Close()
		return nil, err
	}

	return &storage{
		pool:  pool,
		index: index,
	}, nil
}

// close закрывает соединения с базой.
func (s *storage) close() {
	s.pool.Close()
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

func (s *server) geoIP() ports.GeoIP {
	if s.cfg.DaData.APIKey == "" {
		return nil
	}
	source := dadata.New(s.cfg.DaData.APIKey, dadata.Endpoint)
	return redis.NewGeoIP(s.cache, source, keyPrefix, s.cfg.DaData.CacheSecret)
}
