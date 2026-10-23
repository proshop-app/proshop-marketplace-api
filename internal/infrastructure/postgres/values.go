package postgres

import (
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	codeUniqueViolation     = "23505"
	codeForeignKeyViolation = "23503"
)

// saved — итог записи агрегата проекции: конфликт версии, ошибка базы или отметка о сохранении.
func saved(err error, conflict bool, mark func(), subject string, tenantID ids.TenantID, id any) error {
	if conflict {
		return catalog.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("%s %s/%v: %w", subject, tenantID, id, err)
	}

	mark()

	return nil
}

func isUnique(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != codeUniqueViolation {
		return false
	}

	return constraint == "" || pgErr.ConstraintName == constraint
}

func isForeignKey(err error) bool {
	var pgErr *pgconn.PgError

	return errors.As(err, &pgErr) && pgErr.Code == codeForeignKeyViolation
}

func notFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// jsonArg — сырой JSON как параметр jsonb; пусто — NULL.
func jsonArg(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}

	return string(raw)
}

// nullTime — время как параметр; нулевое — NULL.
func nullTime(at time.Time) any {
	if at.IsZero() {
		return nil
	}

	return at
}
