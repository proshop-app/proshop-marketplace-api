package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// commandConnections — пул служебной команды (миграции, таксономия, реестр, отчёты):
// она работает последовательно, второе соединение — под блокировку миграций goose.
const commandConnections = 2

// databaseClasses — классы нагрузки, для каждого из которых процесс держит свой пул.
var databaseClasses = []ports.DatabaseClass{
	ports.DatabaseRead,
	ports.DatabaseCheckout,
	ports.DatabaseIngestion,
	ports.DatabaseBackground,
	ports.DatabaseOrders,
}

// PoolConfig — размер и сроки пула одного класса нагрузки.
type PoolConfig struct {
	MaxConns int32
	Budget   Budget
}

// Pools — отдельный пул PostgreSQL на класс нагрузки. Размер пула — жёсткая квота
// класса: волна приёма и фоновая работа ждут соединения своего пула и не занимают
// соединения запросов покупателей. Операция берёт пул по классу из контекста.
type Pools struct {
	classes map[ports.DatabaseClass]*classPool
}

// OpenPools открывает пулы всех классов; без пула любого класса процесс не стартует.
func OpenPools(ctx context.Context, url string, configs map[ports.DatabaseClass]PoolConfig) (*Pools, error) {
	pools := &Pools{classes: make(map[ports.DatabaseClass]*classPool, len(databaseClasses))}
	for _, class := range databaseClasses {
		config, ok := configs[class]
		if !ok {
			pools.Close()
			return nil, fmt.Errorf("нет настроек пула класса %d", class)
		}
		pool, err := Open(ctx, url, config.MaxConns, config.Budget)
		if err != nil {
			pools.Close()
			return nil, err
		}
		pools.classes[class] = newClassPool(pool, config.Budget)
	}
	return pools, nil
}

// OpenCommand — пул служебной команды со сроками обслуживания budget.
func OpenCommand(ctx context.Context, url string, budget Budget) (*pgxpool.Pool, error) {
	return Open(ctx, url, commandConnections, budget)
}

// Pool — пул класса для хранилищ одного класса нагрузки.
func (p *Pools) Pool(class ports.DatabaseClass) *pgxpool.Pool {
	return p.classes[class].pool
}

// Ping проверяет связь через пул класса из контекста: готовность — пулом
// запросов покупателей, приём — своим.
func (p *Pools) Ping(ctx context.Context) error {
	return p.route(ctx).pool.Ping(ctx)
}

// Close закрывает адаптеры database/sql и пулы.
func (p *Pools) Close() {
	for _, pool := range p.classes {
		_ = pool.db.Close()
		pool.pool.Close()
	}
}

func (p *Pools) route(ctx context.Context) *classPool {
	return p.classes[ports.DatabaseClassFrom(ctx)]
}
