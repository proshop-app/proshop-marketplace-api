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
	route(ctx context.Context) *classPool
}

// classPool — пул одного класса нагрузки и его сроки. Адаптер database/sql один на
// весь срок пула: без простаивающих соединений он не удерживает соединения pgxpool,
// а операция не создаёт свой *sql.DB с горутиной открытия соединений.
type classPool struct {
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
	// dedicated — собственный *sql.DB выделенного соединения; у соединения пула nil.
	dedicated *sql.DB
}

func newConnectionSource(pool *pgxpool.Pool) *connectionSource {
	return &connectionSource{pools: newClassPool(pool, poolBudget(pool))}
}

func newClassPool(pool *pgxpool.Pool, budget Budget) *classPool {
	return &classPool{pool: pool, db: stdlib.OpenDBFromPool(pool), budget: budget}
}

func (p *classPool) route(context.Context) *classPool { return p }

func (s *connectionSource) open(ctx context.Context, pool *classPool) (*gormConnection, error) {
	connection, err := pool.db.Conn(ctx)
	if err != nil {
		return nil, err
	}

	return s.attach(ctx, connection, nil)
}

// Session lock не занимает квоту пула: его владелец может обращаться к базе
// через UnitOfWork даже при MaxConns=1. Закрытие уничтожает выделенное соединение.
func (s *connectionSource) openDedicated(ctx context.Context) (*gormConnection, error) {
	config := s.pools.route(ctx).pool.Config()
	var options []stdlib.OptionOpenDB
	if config.BeforeConnect != nil {
		options = append(options, stdlib.OptionBeforeConnect(config.BeforeConnect))
	}
	if config.AfterConnect != nil {
		options = append(options, stdlib.OptionAfterConnect(config.AfterConnect))
	}
	db := stdlib.OpenDB(*config.ConnConfig, options...)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)

	connection, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()

		return nil, err
	}

	return s.attach(ctx, connection, db)
}

func (s *connectionSource) attach(
	ctx context.Context,
	connection *sql.Conn,
	dedicated *sql.DB,
) (*gormConnection, error) {
	result := &gormConnection{connection: connection, dedicated: dedicated}
	orm, err := s.session(ctx, connection)
	if err != nil {
		result.close()

		return nil, err
	}
	result.orm = orm

	return result, nil
}

func (s *connectionSource) begin(ctx context.Context, pool *classPool) (*gormConnection, error) {
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

// close возвращает соединение пулу; выделенное соединение закрывается вместе со своим *sql.DB.
func (connection *gormConnection) close() {
	_ = connection.connection.Close()
	if connection.dedicated != nil {
		_ = connection.dedicated.Close()
	}
}
