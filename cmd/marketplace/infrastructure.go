package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/searchapi"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/smsru"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

const (
	// indexBatch — предложений за один проход синхронизации индекса.
	indexBatch = 500
	// retentionBatch — записей за один проход чистки.
	retentionBatch = 1000
	// keyPrefix — префикс ключей маркетплейса в Redis.
	keyPrefix = "marketplace:"
	// commandConnections — пул служебной команды: она работает последовательно.
	commandConnections = 2
	// maxProcessConnections — потолок суммы пулов процесса: столько соединений
	// реплика может держать в PostgreSQL одновременно, не считая advisory lock.
	maxProcessConnections = 1000
)

// storage — пулы базы по классам нагрузки и API поиска.
type storage struct {
	pools *postgres.Pools
	index *searchapi.Index
}

// openStorage подключает пулы PostgreSQL по классам нагрузки и API поиска. Поиск
// при этом не вызывается: процесс стартует и без него.
func openStorage(ctx context.Context, cfg config.Config) (*storage, error) {
	classes, err := databasePools(cfg)
	if err != nil {
		return nil, err
	}
	pools, err := postgres.OpenPools(ctx, cfg.Database.URL, classes)
	if err != nil {
		return nil, err
	}

	index, err := openSearch(cfg)
	if err != nil {
		pools.Close()
		return nil, err
	}

	return &storage{
		pools: pools,
		index: index,
	}, nil
}

// close закрывает соединения с базой.
func (s *storage) close() {
	s.pools.Close()
}

// pool — пул класса для хранилищ, которыми пользуется один класс нагрузки.
func (s *storage) pool(class ports.DatabaseClass) *pgxpool.Pool {
	return s.pools.Pool(class)
}

// databasePools — пул на класс нагрузки; сумма пулов ограничена потолком процесса.
func databasePools(cfg config.Config) (map[ports.DatabaseClass]postgres.PoolConfig, error) {
	db := cfg.Database
	ingestion, err := ingestionConnections(cfg)
	if err != nil {
		return nil, err
	}
	db.Ingestion.MaxConns = ingestion
	classes := map[ports.DatabaseClass]config.DatabasePool{
		ports.DatabaseRead:       db.Read,
		ports.DatabaseCheckout:   db.Checkout,
		ports.DatabaseIngestion:  db.Ingestion,
		ports.DatabaseBackground: db.Background,
	}
	pools := make(map[ports.DatabaseClass]postgres.PoolConfig, len(classes))
	var total int32
	for class, pool := range classes {
		total += pool.MaxConns
		pools[class] = postgres.PoolConfig{MaxConns: pool.MaxConns, Budget: postgres.Budget(pool.Budget)}
	}
	if total > maxProcessConnections {
		return nil, fmt.Errorf("DATABASE_*_MAX_CONNS: сумма пулов %d больше %d", total, maxProcessConnections)
	}

	return pools, nil
}

// ingestionConnections — пул приёма вмещает все воркеры Kafka вместе с полосой статусов
// заказов: воркер держит соединение всю транзакцию проекции, и меньший пул превратил бы
// воркеры в очередь за соединением. Без DATABASE_INGESTION_MAX_CONNS пул равен их числу.
func ingestionConnections(cfg config.Config) (int32, error) {
	workers := cfg.Kafka.Workers + kafka.OrderWorkers
	if workers > maxProcessConnections {
		return 0, fmt.Errorf("KAFKA_WORKERS: воркеров приёма больше %d соединений процесса", maxProcessConnections)
	}
	configured := cfg.Database.Ingestion.MaxConns
	if configured == 0 {
		return int32(workers), nil //nolint:gosec // не больше maxProcessConnections
	}
	if int(configured) < workers {
		return 0, fmt.Errorf(
			"DATABASE_INGESTION_MAX_CONNS: не меньше %d — KAFKA_WORKERS и %d воркеров статусов заказов",
			workers,
			kafka.OrderWorkers,
		)
	}
	return configured, nil
}

// openCommandPool — небольшой пул служебной команды со сроками обслуживания.
func openCommandPool(ctx context.Context, database config.Database) (*pgxpool.Pool, error) {
	return postgres.Open(ctx, database.URL, commandConnections, postgres.Budget(database.Maintenance))
}

func openSearch(cfg config.Config) (*searchapi.Index, error) {
	return searchapi.New(searchapi.Config{
		URL:           cfg.Search.URL,
		Token:         cfg.Search.Token,
		PurchaseToken: cfg.Search.PurchaseToken,
		Timeout:       cfg.Search.Timeout,
	})
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
