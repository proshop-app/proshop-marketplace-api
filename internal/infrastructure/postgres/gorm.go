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

// connectionSource разделяет кэши ORM, но не удерживает соединения вне операции.
type connectionSource struct {
	pool        *pgxpool.Pool
	ormOnce     sync.Once
	ormTemplate *gorm.DB
	ormError    error
}

type gormConnection struct {
	orm        *gorm.DB
	connection *sql.Conn
	db         *sql.DB
}

func newConnectionSource(pool *pgxpool.Pool) *connectionSource {
	return &connectionSource{pool: pool}
}

func (s *connectionSource) open(ctx context.Context) (*gormConnection, error) {
	return s.connect(ctx, stdlib.OpenDBFromPool(s.pool))
}

// Session lock не занимает квоту пула: его владелец может обращаться к базе
// через UnitOfWork даже при MaxConns=1. Закрытие уничтожает выделенное соединение.
func (s *connectionSource) openDedicated(ctx context.Context) (*gormConnection, error) {
	config := s.pool.Config()
	var options []stdlib.OptionOpenDB
	if config.BeforeConnect != nil {
		options = append(options, stdlib.OptionBeforeConnect(config.BeforeConnect))
	}
	if config.AfterConnect != nil {
		options = append(options, stdlib.OptionAfterConnect(config.AfterConnect))
	}

	return s.connect(ctx, stdlib.OpenDB(*config.ConnConfig, options...))
}

func (s *connectionSource) connect(ctx context.Context, db *sql.DB) (*gormConnection, error) {
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)

	connection, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()

		return nil, err
	}

	orm, err := s.session(ctx, connection)
	if err != nil {
		_ = connection.Close()
		_ = db.Close()

		return nil, err
	}

	return &gormConnection{
		orm:        orm,
		connection: connection,
		db:         db,
	}, nil
}

func (s *connectionSource) begin(ctx context.Context) (*gormConnection, error) {
	connection, err := s.open(ctx)
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

func (s *connectionSource) withConnection(ctx context.Context, run func(*gorm.DB) error) error {
	connection, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer connection.close()

	return run(connection.orm)
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

// close возвращает соединение исходному pgxpool после завершения операции.
func (connection *gormConnection) close() {
	_ = connection.connection.Close()
	_ = connection.db.Close()
}
