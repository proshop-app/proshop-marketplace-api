package postgres

import (
	"context"
	"database/sql"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

var errBatchConnection = errs.New(
	errs.Internal,
	"storage.batch_connection",
	"пакетная запись требует соединения транзакции",
)

// postgresStatement сохраняет нумерованные параметры PostgreSQL без раскрытия
// массивов и разбора JSONB-операторов ?/@ как плейсхолдеров ORM.
type postgresStatement struct {
	query string
	args  []any
}

func (statement postgresStatement) Build(builder clause.Builder) {
	_, _ = builder.WriteString(statement.query)
	for _, argument := range statement.args {
		// Безымянный NamedArg добавляет bound value без нового плейсхолдера.
		builder.AddVar(builder, sql.Named("", argument))
	}
}

func rawSQL(ctx context.Context, db *gorm.DB, query string, args ...any) *gorm.DB {
	return db.WithContext(ctx).Raw("?", postgresStatement{
		query: query,
		args:  args,
	})
}

func execSQL(ctx context.Context, db *gorm.DB, query string, args ...any) (int64, error) {
	result := db.WithContext(ctx).Exec("?", postgresStatement{
		query: query,
		args:  args,
	})

	return result.RowsAffected, result.Error
}

func sendBatch(
	ctx context.Context,
	db *gorm.DB,
	batch *pgx.Batch,
	consume func(pgx.BatchResults) error,
) error {
	value, ok := db.Get(batchConnectionKey)
	if !ok {
		return errBatchConnection
	}
	connection, ok := value.(*sql.Conn)
	if !ok {
		return errBatchConnection
	}

	// Raw удерживает mutex драйвера до закрытия всей пачки: автоматический
	// rollback при отмене контекста не может одновременно обратиться к pgx.Conn.
	var panicValue any
	panicked := false
	err := connection.Raw(func(value any) (err error) {
		// Паника должна выйти за пределы Raw после освобождения mutex: иначе
		// database/sql попытается закрыть Conn до отката активной транзакции.
		defer func() {
			if value := recover(); value != nil {
				panicValue = value
				panicked = true
			}
		}()

		connection := value.(*stdlib.Conn).Conn()
		results := connection.SendBatch(ctx, batch)
		defer func() {
			if closeErr := results.Close(); err == nil {
				err = closeErr
			}
		}()

		return consume(results)
	})
	if panicked {
		panic(panicValue)
	}

	return err
}
