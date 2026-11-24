package postgres

import (
	"context"
	"database/sql"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const batchConnectionKey = "postgres.batch_connection"

// poolRouter выбирает пул операции: единственный пул или пул класса нагрузки из контекста.
type poolRouter interface {
	route(ctx context.Context) *ClassPool
}

// ClassPool — пул одного класса нагрузки и его сроки. Сроки — свойство пула, а не
// параметры соединения: через пулер их задаёт роль класса, и восстановить их из
// параметров нельзя. Адаптер database/sql один на весь срок пула: без простаивающих
// соединений он не удерживает соединения pgxpool, а операция не создаёт свой *sql.DB
// с горутиной открытия соединений.
type ClassPool struct {
	pool   *pgxpool.Pool
	db     *sql.DB
	budget Budget
}

// connectionSource разделяет кэши ORM, но не удерживает соединения вне операции.
type connectionSource struct {
	pools       poolRouter
	ormOnce     sync.Once
	ormTemplate *gorm.DB
	ormError    error
}

type gormConnection struct {
	orm        *gorm.DB
	connection *sql.Conn
}

// Single — пул, открытый напрямую (Open, OpenCommand: CLI и тесты), со сроками из
// параметров его соединений.
func Single(pool *pgxpool.Pool) *ClassPool {
	return newClassPool(pool, poolBudget(pool))
}

func newConnectionSource(pool *ClassPool) *connectionSource {
	return &connectionSource{pools: pool}
}

func newClassPool(pool *pgxpool.Pool, budget Budget) *ClassPool {
	return &ClassPool{pool: pool, db: stdlib.OpenDBFromPool(pool), budget: budget}
}

func (p *ClassPool) route(context.Context) *ClassPool { return p }

func (s *connectionSource) open(ctx context.Context, pool *ClassPool) (*gormConnection, error) {
	connection, err := pool.db.Conn(ctx)
	if err != nil {
		return nil, err
	}

	result := &gormConnection{connection: connection}
	orm, err := s.session(ctx, connection)
	if err != nil {
		result.close()

		return nil, err
	}
	result.orm = orm

	return result, nil
}

func (s *connectionSource) begin(ctx context.Context, pool *ClassPool) (*gormConnection, error) {
	connection, err := s.open(ctx, pool)
	if err != nil {
		return nil, err
	}

	connection.orm = connection.orm.Begin()
	if connection.orm.Error != nil {
		connection.close()

		return nil, connection.orm.Error
	}
	// Batch использует sql.Conn той же транзакции под блокировкой database/sql.
	connection.orm = connection.orm.Set(batchConnectionKey, connection.connection)

	return connection, nil
}

func (s *connectionSource) withConnection(ctx context.Context, run func(context.Context, *gorm.DB) error) error {
	pool := s.pools.route(ctx)
	ctx, cancel := context.WithTimeout(ctx, pool.budget.Transaction)
	defer cancel()
	connection, err := s.open(ctx, pool)
	if err != nil {
		return err
	}
	defer connection.close()

	return run(ctx, connection.orm)
}

func (s *connectionSource) session(ctx context.Context, connection *sql.Conn) (*gorm.DB, error) {
	s.ormOnce.Do(func() {
		dialector := &gormpostgres.Dialector{Config: &gormpostgres.Config{Conn: connection}}
		s.ormTemplate, s.ormError = gorm.Open(dialector, &gorm.Config{
			// Транзакцией управляет UnitOfWork; отдельная запись не открывает свою.
			SkipDefaultTransaction: true,
			DisableAutomaticPing:   true,
			QueryFields:            true,
			// SQL с персональными данными не должен попадать в журнал ORM.
			Logger: logger.Default.LogMode(logger.Silent),
		})
		if s.ormError == nil {
			// Шаблон хранит общие кэши схем и callbacks, а не первое соединение.
			s.ormTemplate.ConnPool = nil
			s.ormTemplate.Statement.ConnPool = nil
			dialector.Conn = nil
		}
	})
	if s.ormError != nil {
		return nil, s.ormError
	}

	orm := s.ormTemplate.Session(&gorm.Session{
		NewDB:   true,
		Context: ctx,
	})
	orm.ConnPool = connection
	orm.Statement.ConnPool = connection

	return orm, nil
}

// close возвращает соединение пулу.
func (connection *gormConnection) close() {
	_ = connection.connection.Close()
}
