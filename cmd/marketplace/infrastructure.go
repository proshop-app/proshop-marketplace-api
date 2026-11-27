package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
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
	// maxProcessConnections — потолок суммы пулов процесса: столько клиентских соединений
	// реплика может держать к базе или пулеру одновременно, не считая сеансов блокировок.
	maxProcessConnections = 1000
	// outboxHandlerTimeout — срок обработчика задачи outbox. Самый долгий вызов внутри —
	// служебная запись в поиск (searchapi.ServiceTimeout), запас — на чтение и запись
	// базы вокруг неё: вызов всегда укладывается в срок обработчика.
	outboxHandlerTimeout = searchapi.ServiceTimeout + 5*time.Second
)

// storage — пулы базы по классам нагрузки, прямые сеансы под блокировки и API поиска.
type storage struct {
	pools    *postgres.Pools
	sessions *postgres.DirectSessions
	index    *searchapi.Index
}

// openStorage подключает пулы PostgreSQL по классам нагрузки и API поиска. Поиск
// при этом не вызывается: процесс стартует и без него.
func openStorage(ctx context.Context, cfg config.Config) (*storage, error) {
	classes, err := databasePools(cfg)
	if err != nil {
		return nil, err
	}
	// Сеанс блокировки исполняет только взятие и проверку владельца: сроки фонового класса.
	sessions, err := postgres.NewDirectSessions(cfg.Database.Direct(), postgres.Budget(cfg.Database.Background.Budget))
	if err != nil {
		return nil, err
	}
	target := postgres.Target{URL: cfg.Database.URL, Pooled: cfg.Database.Pooled()}
	pools, err := postgres.OpenPools(ctx, target, classes)
	if err != nil {
		return nil, err
	}

	index, err := openSearch(cfg)
	if err != nil {
		pools.Close()
		return nil, err
	}

	return &storage{
		pools:    pools,
		sessions: sessions,
		index:    index,
	}, nil
}

// close закрывает соединения с базой.
func (s *storage) close() {
	s.pools.Close()
}

// pool — пул класса для хранилищ, которыми пользуется один класс нагрузки.
func (s *storage) pool(class ports.DatabaseClass) *postgres.ClassPool {
	return s.pools.Class(class)
}

// databasePools — пул на класс нагрузки; сумма пулов ограничена потолком процесса.
func databasePools(cfg config.Config) (map[ports.DatabaseClass]postgres.PoolConfig, error) {
	db := cfg.Database
	var err error
	// Воркер приёма держит соединение всю транзакцию проекции: меньший пул превратил бы
	// воркеры в очередь за соединением.
	db.Ingestion.MaxConns, err = fitConnections(
		"DATABASE_INGESTION_MAX_CONNS",
		db.Ingestion.MaxConns,
		cfg.Kafka.Workers+kafka.OrderWorkers,
		"KAFKA_WORKERS и воркеров статусов заказов",
	)
	if err != nil {
		return nil, err
	}
	// Каждое место исполнителя держит не больше одного соединения, ещё одно — взятие задач:
	// при таком пуле ни одно место не ждёт соединения за другим.
	db.Orders.MaxConns, err = fitConnections(
		"DATABASE_ORDERS_MAX_CONNS",
		db.Orders.MaxConns,
		appcheckout.ExecutorSlots+1,
		"мест исполнителя оформления и взятия задач",
	)
	if err != nil {
		return nil, err
	}
	classes := map[ports.DatabaseClass]config.DatabasePool{
		ports.DatabaseRead:       db.Read,
		ports.DatabaseCheckout:   db.Checkout,
		ports.DatabaseWrite:      db.Write,
		ports.DatabaseIngestion:  db.Ingestion,
		ports.DatabaseBackground: db.Background,
		ports.DatabaseOrders:     db.Orders,
	}
	pools := make(map[ports.DatabaseClass]postgres.PoolConfig, len(classes))
	var total int32
	for class, pool := range classes {
		total += pool.MaxConns
		pools[class] = postgres.PoolConfig{
			MaxConns: pool.MaxConns,
			Budget:   postgres.Budget(pool.Budget),
			User:     pool.User,
			Password: pool.Password,
		}
	}
	if total > maxProcessConnections {
		return nil, fmt.Errorf("DATABASE_*_MAX_CONNS: сумма пулов %d больше %d", total, maxProcessConnections)
	}

	return pools, nil
}

// fitConnections — пул под одновременных держателей соединений: без переменной он равен
// их числу, меньше — отказ старта.
func fitConnections(name string, configured int32, holders int, who string) (int32, error) {
	if holders > maxProcessConnections {
		return 0, fmt.Errorf("%s: %d держателей соединений больше %d", name, holders, maxProcessConnections)
	}
	if configured == 0 {
		return int32(holders), nil //nolint:gosec // не больше maxProcessConnections
	}
	if int(configured) < holders {
		return 0, fmt.Errorf("%s: не меньше %d — %s", name, holders, who)
	}
	return configured, nil
}

// checkOutboxLease — обработчик укладывается в половину аренды (правило диспетчера):
// вторая половина — отметка итога прохода и запас; иначе задачу, которую ещё пишет
// эта реплика, взял бы сосед.
func checkOutboxLease(outbox config.Outbox) error {
	if outbox.Lease < 2*outboxHandlerTimeout {
		return fmt.Errorf("OUTBOX_LEASE: не меньше %s — двух сроков обработчика outbox", 2*outboxHandlerTimeout)
	}
	return nil
}

// openCommandPool — небольшой пул служебной команды со сроками обслуживания, по прямому
// адресу: команды держат сеансовые блокировки.
func openCommandPool(ctx context.Context, database config.Database) (*pgxpool.Pool, error) {
	return postgres.OpenCommand(ctx, database.Direct(), postgres.Budget(database.Maintenance))
}

func openSearch(cfg config.Config) (*searchapi.Index, error) {
	return searchapi.New(searchapi.Config{
		URL:           cfg.Search.URL,
		Token:         cfg.Search.Token,
		PurchaseToken: cfg.Search.PurchaseToken,
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
